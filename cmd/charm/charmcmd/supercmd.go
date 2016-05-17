// Copyright 2016 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd

import (
	"github.com/juju/cmd"
)

// commandWithPlugins is a cmd.Command with the ability to check if some
// provided args refer to a registered plugin, and in that case, to properly
// initialize the plugin.
type commandWithPlugins interface {
	cmd.Command
	isPlugin(args []string) bool
	initPlugin(name string, args []string)
}

// newSuperCommand creates and returns a new superCommand wrapping the given
// cmd.SuperCommand.
func newSuperCommand(supercmd *cmd.SuperCommand) *superCommand {
	return &superCommand{
		SuperCommand: supercmd,
		all: map[string]bool{
			// The help command is registered by the super command.
			"help": true,
		},
	}
}

// superCommand is a cmd.SuperCommand that can keep track of registered
// subcommands and plugins. superCommand implements commandWithPlugins.
type superCommand struct {
	*cmd.SuperCommand
	plugins map[string]cmd.Command
	all     map[string]bool
}

// register registers the given command so that it is made available to be used
// from the super command.
func (s *superCommand) register(command cmd.Command) {
	s.SuperCommand.Register(command)
	s.all[command.Info().Name] = true
}

// registerPlugins registers all found plugins as subcommands.
func (s *superCommand) registerPlugins() {
	plugins := getPluginDescriptions()
	s.plugins = make(map[string]cmd.Command, len(plugins))
	for _, plugin := range plugins {
		if s.isAvailable(plugin.Name) {
			command := &pluginCommand{
				name:    plugin.Name,
				purpose: plugin.Description,
				doc:     plugin.Doc,
			}
			s.register(command)
			s.plugins[plugin.Name] = command
		}
	}
	s.SuperCommand.AddHelpTopicCallback(
		"plugins", "Show "+s.SuperCommand.Name+" plugins", pluginHelpTopic)
}

// isAvailable reports whether the given command name is available, meaning
// it has not been already registered.
func (s *superCommand) isAvailable(name string) bool {
	return !s.all[name]
}

// isPlugin reports whether the given super command arguments call a plugin.
func (s *superCommand) isPlugin(args []string) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := s.plugins[args[0]]
	return ok
}

// initPlugin initialize a registered plugin using the given args.
func (s *superCommand) initPlugin(name string, args []string) {
	s.plugins[name].Init(args)
}
