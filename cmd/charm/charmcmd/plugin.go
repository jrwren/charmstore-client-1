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
		for _, plugin := range existingPlugins {
			if len(plugin.Name) > longest {
				longest = len(plugin.Name)
			}
		}
		for _, plugin := range existingPlugins {
			fmt.Fprintf(output, "%-*s  %s\n", longest, plugin.Name, plugin.Description)
		}
	}
	return output.String()
}

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
	plugins, pluginExists := findPlugins()
	results := map[string]pluginDescription{}
	if len(plugins) == 0 {
		return results
	}
	if err := os.MkdirAll(pluginCacheDir, os.ModeDir|os.ModePerm); err != nil {
		logger.Errorf("creating plugin cache dir: %s, %s", pluginCacheDir, err)
	}
	pluginCache := openCache(pluginCacheFile)
	c := readAndReturnCacheIfValid(pluginCache, plugins, pluginExists)
	if c != nil {
		pluginDescriptionLastCallReturnedCache = true
		return c
	}
	pluginDescriptionLastCallReturnedCache = false

	// Create a channel with enough backing for each plugin.
	description := make(chan pluginDescription, len(plugins))
	help := make(chan pluginDescription, len(plugins))

	// Exec the --description and --help commands.
	for _, plugin := range plugins {
		fi := plugin
		go func() {
			result := pluginDescription{
				Name:    fi.name,
				ModTime: fi.mtime,
			}
			defer func() {
				description <- result
			}()
			desccmd := exec.Command(fi.name, "--description")
			output, err := desccmd.CombinedOutput()

			if err == nil {
				// Trim to only get the first line.
				result.Description = strings.SplitN(string(output), "\n", 2)[0]
			} else {
				result.Description = fmt.Sprintf("error occurred running '%s --description'", fi.name)
				logger.Debugf("'%s --description': %s", fi.name, err)
			}
		}()
		go func() {
			result := pluginDescription{
				Name: fi.name,
			}
			defer func() {
				help <- result
			}()
			helpcmd := exec.Command(fi.name, "--help")
			output, err := helpcmd.CombinedOutput()
			if err == nil {
				result.Doc = string(output)
			} else {
				result.Doc = fmt.Sprintf("error occured running '%s --help'", fi.name)
				logger.Debugf("'%s --help': %s", fi.name, err)
			}
		}()
	}
	resultDescriptionMap := map[string]pluginDescription{}
	resultHelpMap := map[string]pluginDescription{}
	// Gather the results at the end.
	for _ = range plugins {
		result := <-description
		resultDescriptionMap[result.Name] = result
		helpResult := <-help
		resultHelpMap[helpResult.Name] = helpResult
	}
	// plugins array is already sorted, use this to get the results in order.
	for _, plugin := range plugins {
		// Strip the 'charm-' off the start of the plugin name in the results.
		result := resultDescriptionMap[plugin.name]
		result.Name = result.Name[len(pluginPrefix):]
		result.Doc = resultHelpMap[plugin.name].Doc
		results[filepath.Join(plugin.dir, plugin.name)] = result
	}
	pluginDescriptionsResults = results
	pluginCache.Plugins = results
	pluginCache.save(pluginCacheFile)
	return results
}

func readAndReturnCacheIfValid(pc *pluginCache, plugins []fileInfo, pluginExists map[string]int) map[string]pluginDescription {
	cachedPlugins := pc.Plugins
	if cachedPlugins == nil {
		return nil
	}
	cachedExists := map[string]int{}
	for _, cachedPlugin := range cachedPlugins {
		cachedExists[pluginPrefix+cachedPlugin.Name]++
		// If a plugin no longer exists in cache, invalidate entire cache.
		if pluginExists[pluginPrefix+cachedPlugin.Name] == 0 {
			return nil
		}
		// Compare ModTime of each found plugin to its cached plugin.
		for _, newp := range plugins {
			if newp.name == pluginPrefix+cachedPlugin.Name {
				p, _ := pc.isCurrent(filepath.Join(newp.dir, newp.name))
				if p == nil {
					return nil
				}
			}
		}
	}
	for _, newp := range plugins {
		// If the cache is missing a found plugin, invalidate entire cache.
		if cachedExists[newp.name] == 0 {
			return nil
		}
	}

	// The cache was not invalidated for any reason, so use it.
	return cachedPlugins
}

type pluginCache struct {
	Plugins map[string]pluginDescription
}

func openCache(file string) *pluginCache {
	f, err := os.Open(file)
	var c pluginCache
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

func (c *pluginCache) isCurrent(filename string) (*pluginDescription, error) {
	stat, err := os.Stat(filename)
	if err != nil {
		logger.Errorf("could not stat %s", filename, err)
		return nil, err
	}
	if stat.ModTime() != c.Plugins[filename].ModTime {
		// We could invalidate just this plugin, but it is a rare occurence so invalidate all.
		return nil, nil
	}
	p := c.Plugins[filename]
	return &p, nil
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
