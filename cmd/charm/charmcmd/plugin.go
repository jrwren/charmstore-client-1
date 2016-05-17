// Copyright 2015 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

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

// pluginDescriptionsResults holds memoized results for getPluginDescriptions.
var pluginDescriptionsResults []pluginDescription

// getPluginDescriptions runs each plugin with "--description".  The calls to
// the plugins are run in parallel, so the function should only take as long
// as the longest call.
func getPluginDescriptions() []pluginDescription {
	pluginCacheDir := os.Getenv("HOME") + "/.cache"
	pluginCache := pluginCacheDir + "/charm-command-cache"
	if len(pluginDescriptionsResults) > 0 {
		return pluginDescriptionsResults
	}
	plugins := findPlugins()
	results := []pluginDescription{}
	if len(plugins) == 0 {
		return results
	}
	if err := os.MkdirAll(pluginCacheDir, os.ModeDir); err != nil {
		logger.Errorf("creating plugin Cache Dir: %s, %s", pluginCacheDir, err)
	}
	if f, err := os.Open(pluginCache); err == nil {
		decoder := json.NewDecoder(f)
		if err = decoder.Decode(&results); err == nil {
			return results
		}
	}
	// Create a channel with enough backing for each plugin.
	description := make(chan pluginDescription, len(plugins))
	help := make(chan pluginDescription, len(plugins))

	// Exec the --description and --help commands.
	for _, plugin := range plugins {
		go func(plugin string) {
			result := pluginDescription{
				Name: plugin,
			}
			defer func() {
				description <- result
			}()
			desccmd := exec.Command(plugin, "--description")
			output, err := desccmd.CombinedOutput()

			if err == nil {
				// Trim to only get the first line.
				result.Description = strings.SplitN(string(output), "\n", 2)[0]
			} else {
				result.Description = fmt.Sprintf("error occurred running '%s --description'", plugin)
				logger.Debugf("'%s --description': %s", plugin, err)
			}
		}(plugin)
		go func(plugin string) {
			result := pluginDescription{
				Name: plugin,
			}
			defer func() {
				help <- result
			}()
			helpcmd := exec.Command(plugin, "--help")
			output, err := helpcmd.CombinedOutput()
			if err == nil {
				result.Doc = string(output)
			} else {
				result.Doc = fmt.Sprintf("error occured running '%s --help'", plugin)
				logger.Debugf("'%s --help': %s", plugin, err)
			}
		}(plugin)
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
		result := resultDescriptionMap[plugin]
		result.Name = result.Name[len(pluginPrefix):]
		result.Doc = resultHelpMap[plugin].Doc
		results = append(results, result)
	}
	pluginDescriptionsResults = results

	if f, err := os.Create(pluginCache); err == nil {
		encoder := json.NewEncoder(f)
		if err = encoder.Encode(results); err == nil {
			logger.Errorf("encoding cached plugin descriptions: %s", err)
		}
	} else {
		logger.Errorf("opening plugin cache file: %s", err)
	}
	return results
}

type pluginDescription struct {
	Name        string
	Description string
	Doc         string
}

// findPlugins searches the current PATH for executable files that start with
// pluginPrefix.
func findPlugins() []string {
	path := os.Getenv("PATH")
	plugins := []string{}
	for _, name := range filepath.SplitList(path) {
		entries, err := ioutil.ReadDir(name)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), pluginPrefix) && (entry.Mode()&0111) != 0 {
				plugins = append(plugins, entry.Name())
			}
		}
	}
	sort.Strings(plugins)
	return plugins
}
