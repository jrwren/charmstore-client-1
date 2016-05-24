// Copyright 2015-2016 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/juju/cmd"
)

const pluginPrefix = cmdName + "-"

func runPlugin(ctx *cmd.Context, subcommand string, args []string) error {
	plugin := &pluginCommand{
		name: subcommand,
	}
	if err := plugin.Init(args); err != nil {
		return err
	}
	err := plugin.Run(ctx)
	_, execError := err.(*exec.Error)
	// exec.Error results are for when the executable isn't found, in
	// those cases, drop through.
	if !execError {
		return err
	}
	return &cmd.UnrecognizedCommand{Name: subcommand}
}

type pluginCommand struct {
	cmd.CommandBase
	name    string
	args    []string
	purpose string
	doc     string
}

// Info returns information about the Command.
func (pc *pluginCommand) Info() *cmd.Info {
	purpose := pc.purpose
	if purpose == "" {
		purpose = "support charm plugins"
	}
	doc := pc.doc
	if doc == "" {
		doc = pluginTopicText
	}
	return &cmd.Info{
		Name:    pc.name,
		Purpose: purpose,
		Doc:     doc,
	}
}

func (c *pluginCommand) Init(args []string) error {
	c.args = args
	return nil
}

func (c *pluginCommand) Run(ctx *cmd.Context) error {
	command := exec.Command(pluginPrefix+c.name, c.args...)
	command.Stdin = ctx.Stdin
	command.Stdout = ctx.Stdout
	command.Stderr = ctx.Stderr
	err := command.Run()
	if exitError, ok := err.(*exec.ExitError); ok && exitError != nil {
		status := exitError.ProcessState.Sys().(syscall.WaitStatus)
		if status.Exited() {
			return cmd.NewRcPassthroughError(status.ExitStatus())
		}
	}
	return err
}

const pluginTopicText = cmdName + ` plugins

Plugins are implemented as stand-alone executable files somewhere in the user's PATH.
The executable command must be of the format ` + cmdName + `-<plugin name>.

`

func pluginHelpTopic() string {
	output := &bytes.Buffer{}
	fmt.Fprintf(output, pluginTopicText)
	existingPlugins := getPluginDescriptions()
	if len(existingPlugins) == 0 {
		fmt.Fprintf(output, "No plugins found.\n")
	} else {
		longest := 0
		keys := make([]string, 0, len(existingPlugins))
		for k, plugin := range existingPlugins {
			if len(plugin.Name) > longest {
				longest = len(plugin.Name)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			plugin := existingPlugins[k]
			fmt.Fprintf(output, "%-*s  %s\n", longest, plugin.Name, plugin.Description)
		}
	}
	return output.String()
}

// pluginDescriptionLastCallReturnedCache is true if all plugins values were cached.
var pluginDescriptionLastCallReturnedCache bool

// pluginDescriptionsResults holds memoized results for getPluginDescriptions.
var pluginDescriptionsResults map[string]pluginDescription

// getPluginDescriptions runs each plugin with "--description".  The calls to
// the plugins are run in parallel, so the function should only take as long
// as the longest call.
// We cache results in $XDG_CACHE_HOME/charm-command-cache
// or $HOME/.cache/charm-command-cache if $XDG_CACHE_HOME
// isn't set, invalidating the cache if executable modification times change.
func getPluginDescriptions() map[string]pluginDescription {
	if len(pluginDescriptionsResults) > 0 {
		return pluginDescriptionsResults
	}
	pluginCacheDir := filepath.Join(os.Getenv("HOME"), ".cache")
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		pluginCacheDir = d
	}
	pluginCacheFile := filepath.Join(pluginCacheDir, "charm-command-cache")
	plugins, _ := findPlugins()
	if len(plugins) == 0 {
		return map[string]pluginDescription{}
	}
	if err := os.MkdirAll(pluginCacheDir, os.ModeDir|os.ModePerm); err != nil {
		logger.Errorf("creating plugin cache dir: %s, %s", pluginCacheDir, err)
	}
	pluginCache := openCache(pluginCacheFile)
	allcached := true
	allcachedLock := sync.RWMutex{}
	wg := sync.WaitGroup{}
	for _, plugin := range plugins {
		wg.Add(1)
		plugin := plugin
		go func() {
			defer wg.Done()
			_, cached := pluginCache.fetch(plugin)
			allcachedLock.Lock()
			allcached = allcached && cached
			allcachedLock.Unlock()
		}()
	}
	wg.Wait()
	pluginDescriptionLastCallReturnedCache = allcached
	pluginCache.save(pluginCacheFile)
	pluginDescriptionsResults = pluginCache.Plugins
	return pluginCache.Plugins
}

type pluginCache struct {
	Plugins     map[string]pluginDescription
	pluginsLock sync.RWMutex
}

func openCache(file string) *pluginCache {
	f, err := os.Open(file)
	var c pluginCache
	c.pluginsLock = sync.RWMutex{}
	if err != nil {
		c = pluginCache{}
		c.Plugins = make(map[string]pluginDescription)
	}
	if err := json.NewDecoder(f).Decode(&c); err != nil {
		c = pluginCache{}
		c.Plugins = make(map[string]pluginDescription)
	}
	return &c
}

// returns pluginDescription and boolean indicating if cache was used.
func (c *pluginCache) fetch(fi fileInfo) (*pluginDescription, bool) {
	filename := filepath.Join(fi.dir, fi.name)
	stat, err := os.Stat(filename)
	if err != nil {
		logger.Errorf("could not stat %s", filename, err)
		// If a file is not readable or otherwise not statable, ignore it.
		return nil, false
	}
	mtime := stat.ModTime()
	c.pluginsLock.RLock()
	p, ok := c.Plugins[filename]
	c.pluginsLock.RUnlock()
	// If the plugin is cached check its mtime.
	if ok {
		// If mtime is same as cached, return the cached data.
		if mtime.Unix() == p.ModTime.Unix() {
			return &p, true
		}
	}
	// The cached data is invalid. Run the plugin.
	result := pluginDescription{
		Name:    fi.name[len(pluginPrefix):],
		ModTime: mtime,
	}
	wg := sync.WaitGroup{}
	desc := ""
	help := ""
	wg.Add(1)
	go func() {
		defer wg.Done()
		desccmd := exec.Command(fi.name, "--description")
		output, err := desccmd.CombinedOutput()

		if err == nil {
			// Trim to only get the first line.
			desc = strings.SplitN(string(output), "\n", 2)[0]
		} else {
			desc = fmt.Sprintf("error occurred running '%s --description'", fi.name)
			logger.Debugf("'%s --description': %s", fi.name, err)
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		helpcmd := exec.Command(fi.name, "--help")
		output, err := helpcmd.CombinedOutput()
		if err == nil {
			help = string(output)
		} else {
			help = fmt.Sprintf("error occured running '%s --help'", fi.name)
			logger.Debugf("'%s --help': %s", fi.name, err)
		}
	}()
	wg.Wait()
	result.Doc = help
	result.Description = desc
	c.pluginsLock.Lock()
	c.Plugins[filename] = result
	c.pluginsLock.Unlock()
	return &result, false
}

func (c *pluginCache) save(filename string) error {
	if f, err := os.Create(filename); err == nil {
		encoder := json.NewEncoder(f)
		if err = encoder.Encode(c); err != nil {
			logger.Errorf("encoding cached plugin descriptions: %s", err)
			return err
		}
	} else {
		logger.Errorf("opening plugin cache file: %s", err)
		return err
	}
	return nil
}

type pluginDescription struct {
	Name        string
	Description string
	Doc         string
	ModTime     time.Time
}

// findPlugins searches the current PATH for executable files that start with
// pluginPrefix.
func findPlugins() ([]fileInfo, map[string]int) {
	path := os.Getenv("PATH")
	plugins := fileInfos{}
	seen := map[string]int{}
	for _, dir := range filepath.SplitList(path) {
		// ioutil.ReadDir uses lstat on every file and returns a different
		// modtime than os.Stat.  Do not use ioutil.ReadDir.
		dirh, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, err := dirh.Readdirnames(0)
		if err != nil {
			continue
		}
		for _, name := range names {
			if seen[name] > 0 {
				continue
			}
			if strings.HasPrefix(name, pluginPrefix) {
				stat, err := os.Stat(filepath.Join(dir, name))
				if err != nil {
					continue
				}
				if (stat.Mode() & 0111) != 0 {
					plugins = append(plugins, fileInfo{
						name:  name,
						mtime: stat.ModTime(),
						dir:   dir,
					})
					seen[name]++
				}
			}
		}
	}
	sort.Sort(plugins)
	return plugins, seen
}

type fileInfo struct {
	name  string
	mtime time.Time
	dir   string
}

type fileInfos []fileInfo

func (a fileInfos) Len() int           { return len(a) }
func (a fileInfos) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a fileInfos) Less(i, j int) bool { return a[i].name < a[j].name }
