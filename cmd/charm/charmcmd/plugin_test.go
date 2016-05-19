// Copyright 2015-2016 Canonical Ltd.
// Licensed under the GPLv3, see LICENCE file for details.

package charmcmd_test

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/juju/testing"
	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"

	"github.com/juju/charmstore-client/cmd/charm/charmcmd"
)

type pluginSuite struct {
	testing.IsolationSuite
	dir  string
	dir2 string
}

var _ = gc.Suite(&pluginSuite{})

func (s *pluginSuite) SetUpTest(c *gc.C) {
	if runtime.GOOS == "windows" {
		c.Skip("tests use bash scripts")
	}
	s.IsolationSuite.SetUpTest(c)
	s.dir = c.MkDir()
	s.dir2 = c.MkDir()
	s.PatchEnvironment("PATH", s.dir+":"+s.dir2)
	charmcmd.ResetPluginDescriptionsResults()
}

func (*pluginSuite) TestPluginHelpNoPlugins(c *gc.C) {
	stdout, stderr := runHelp(c)
	c.Assert(stdout, gc.Equals, "No plugins found.\n")
	c.Assert(stderr, gc.Equals, "")
}

func (s *pluginSuite) TestPluginHelpOrder(c *gc.C) {
	s.makePlugin("foo", 0744)
	s.makePlugin("bar", 0744)
	s.makePlugin("baz", 0744)
	stdout, stderr := runHelp(c)
	c.Assert(stdout, gc.Equals, `bar  bar --description
baz  baz --description
foo  foo --description
`)
	c.Assert(stderr, gc.Equals, "")
}

func (s *pluginSuite) TestPluginHelpIgnoreNotExecutable(c *gc.C) {
	s.makePlugin("foo", 0644)
	s.makePlugin("bar", 0666)
	stdout, stderr := runHelp(c)
	c.Assert(stdout, gc.Equals, "No plugins found.\n")
	c.Assert(stderr, gc.Equals, "")
}

func (s *pluginSuite) TestPluginHelpSpecificCommand(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	stdout, stderr, code := run(c.MkDir(), "help", "foo")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, `Usage: charm foo

Summary:
foo --description

Details:
foo longer help

something useful
`)
}

func (s *pluginSuite) TestPluginHelpCommandNotFound(c *gc.C) {
	stdout, stderr, code := run(c.MkDir(), "help", "foo")
	c.Assert(stderr, gc.Equals, "ERROR unknown command or topic for foo\n")
	c.Assert(code, gc.Equals, 1)
	c.Assert(stdout, gc.Equals, "")
}

func (s *pluginSuite) TestPluginHelpRunInParallel(c *gc.C) {
	// Make plugins that will deadlock if we don't start them in parallel.
	// Each plugin depends on another one being started before they will
	// complete. They make a full loop, so no sequential ordering will ever
	// succeed.
	s.makeFullPlugin(pluginParams{Name: "foo", Creates: "foo", DependsOn: "bar"})
	s.makeFullPlugin(pluginParams{Name: "bar", Creates: "bar", DependsOn: "baz"})
	s.makeFullPlugin(pluginParams{Name: "baz", Creates: "baz", DependsOn: "error"})
	s.makeFullPlugin(pluginParams{Name: "error", ExitStatus: 1, Creates: "error", DependsOn: "foo"})

	// If the code was wrong, getPluginDescriptions would deadlock,
	// so timeout after a short while.
	outputChan := make(chan string)
	go func() {
		stdout, stderr := runHelp(c)
		c.Assert(stderr, gc.Equals, "")
		outputChan <- stdout
	}()
	// 10 seconds is arbitrary but should always be generously long. Test
	// actually only takes about 15ms in practice, but 10s allows for system
	// hiccups, etc.
	wait := 5 * time.Second
	var output string
	select {
	case output = <-outputChan:
	case <-time.After(wait):
		c.Fatalf("took longer than %fs to complete", wait.Seconds())
	}
	c.Assert(output, gc.Equals, `bar    bar --description
baz    baz --description
error  error occurred running 'charm-error --description'
foo    foo --description
`)
}

func (s *pluginSuite) TestPluginRun(c *gc.C) {
	s.makePlugin("foo", 0755)
	stdout, stderr, code := run(c.MkDir(), "foo", "some", "params")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, "foo some params\n")
}

func (s *pluginSuite) TestPluginRunWithError(c *gc.C) {
	s.makeFailingPlugin("foo", 2)
	stdout, stderr, code := run(c.MkDir(), "foo", "some", "params")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 2)
	c.Assert(stdout, gc.Equals, "failing\n")
}

func (s *pluginSuite) TestPluginRunWithHelpFlag(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	stdout, stderr, code := run(c.MkDir(), "foo", "--help")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, `Usage: charm foo

Summary:
foo --description

Details:
foo longer help

something useful
`)
}

func (s *pluginSuite) TestPluginRunWithDebugFlag(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	stdout, stderr, code := run(c.MkDir(), "foo", "--debug")
	c.Assert(stderr, gc.Matches, ".*?INFO cmd supercommand.go:\\d+ command finished\n")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, "some debug\n")
}

func (s *pluginSuite) TestPluginRunWithEnvVars(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	s.PatchEnvironment("ANSWER", "42")
	stdout, stderr, code := run(c.MkDir(), "foo")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, "foo\nanswer is 42\n")
}

func (s *pluginSuite) TestPluginRunWithMultipleNamesInPath(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	s.PatchEnvironment("ANSWER", "42")
	s.makeFullPluginInSecondDir(pluginParams{Name: "foo"})
	stdout, stderr, code := run(c.MkDir(), "foo")
	c.Assert(stderr, gc.Equals, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, "foo\nanswer is 42\n")
}

func (s *pluginSuite) TestPluginRunWithUnknownFlag(c *gc.C) {
	s.makeFullPlugin(pluginParams{Name: "foo"})
	stdout, stderr, code := run(c.MkDir(), "foo", "--unknown-to-juju")
	c.Assert(stderr, gc.Matches, "")
	c.Assert(code, gc.Equals, 0)
	c.Assert(stdout, gc.Equals, "the flag was still there.\n")
}

func (s *pluginSuite) TestPluginCacheCaches(c *gc.C) {
	s.PatchEnvironment("HOME", "/tmp")
	os.Remove("/tmp/.cache/charm-command-cache")
	s.makeFullPlugin(pluginParams{Name: "foo"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
	charmcmd.ResetPluginDescriptionsResults()
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, true)
}

func (s *pluginSuite) TestPluginCacheInvalidatesOnUpdate(c *gc.C) {
	s.PatchEnvironment("HOME", "/tmp")
	os.Remove("/tmp/.cache/charm-command-cache")
	s.makeFullPlugin(pluginParams{Name: "foo"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
	charmcmd.ResetPluginDescriptionsResults()
	time.Sleep(time.Second) // Sleep so that the written file has a different mtime
	s.makeFullPlugin(pluginParams{Name: "foo"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
}

func (s *pluginSuite) TestPluginCacheInvalidatesOnNewPlugin(c *gc.C) {
	s.PatchEnvironment("HOME", "/tmp")
	os.Remove("/tmp/.cache/charm-command-cache")
	s.makeFullPlugin(pluginParams{Name: "foo"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
	charmcmd.ResetPluginDescriptionsResults()
	s.makeFullPlugin(pluginParams{Name: "bar"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
}

func (s *pluginSuite) TestPluginCacheInvalidatesOnDeletedPlugin(c *gc.C) {
	s.PatchEnvironment("HOME", "/tmp")
	os.Remove("/tmp/.cache/charm-command-cache")
	s.makeFullPlugin(pluginParams{Name: "foo"})
	s.makeFullPlugin(pluginParams{Name: "bar"})
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
	charmcmd.ResetPluginDescriptionsResults()
	os.Remove(filepath.Join(s.dir, "charm-bar"))
	run(c.MkDir(), "help", "foo")
	c.Assert(*charmcmd.PluginDescriptionLastCallReturnedCache, gc.Equals, false)
}

func (s *pluginSuite) makePlugin(name string, perm os.FileMode) {
	content := fmt.Sprintf("#!/bin/bash --norc\necho %s $*", name)
	writePlugin(s.dir, name, content, perm)
}

func (s *pluginSuite) makeFailingPlugin(name string, exitStatus int) {
	content := fmt.Sprintf("#!/bin/bash --norc\necho failing\nexit %d", exitStatus)
	writePlugin(s.dir, name, content, 0755)
}

func (s *pluginSuite) makeFullPluginInSecondDir(params pluginParams) {
	makeFullPluginToDir(params, s.dir2)
}

func (s *pluginSuite) makeFullPlugin(params pluginParams) {
	makeFullPluginToDir(params, s.dir)
}

func makeFullPluginToDir(params pluginParams, dir string) {
	// Create a new template and parse the plugin into it.
	t := template.Must(template.New("plugin").Parse(pluginTemplate))
	content := &bytes.Buffer{}
	// Create the files in a temp dir, so we don't pollute the working space.
	if params.Creates != "" {
		params.Creates = filepath.Join(dir, params.Creates)
	}
	if params.DependsOn != "" {
		params.DependsOn = filepath.Join(dir, params.DependsOn)
	}
	if err := t.Execute(content, params); err != nil {
		panic(err)
	}
	writePlugin(dir, params.Name, content.String(), 0755)
}

func writePlugin(dir, name, content string, perm os.FileMode) {
	path := filepath.Join(dir, "charm-"+name)
	if err := ioutil.WriteFile(path, []byte(content), perm); err != nil {
		panic(err)
	}
}

type pluginParams struct {
	Name       string
	ExitStatus int
	Creates    string
	DependsOn  string
}

const pluginTemplate = `#!/bin/bash --norc

if [ "$1" = "--description" ]; then
  if [ -n "{{.Creates}}" ]; then
    /usr/bin/touch "{{.Creates}}"
  fi
  if [ -n "{{.DependsOn}}" ]; then
    # Sleep 10ms while waiting to allow other stuff to do work
    while [ ! -e "{{.DependsOn}}" ]; do /bin/sleep 0.010; done
  fi
  echo "{{.Name}} --description"
  exit {{.ExitStatus}}
fi

if [ "$1" = "--help" ]; then
  echo "{{.Name}} longer help"
  echo ""
  echo "something useful"
  exit {{.ExitStatus}}
fi

if [ "$1" = "--debug" ]; then
  echo "some debug"
  exit {{.ExitStatus}}
fi

if [ "$1" = "--unknown-to-juju" ]; then
  echo "the flag was still there."
  exit {{.ExitStatus}}
fi

echo {{.Name}} $*
echo "answer is $ANSWER"
exit {{.ExitStatus}}
`

func runHelp(c *gc.C) (string, string) {
	stdout, stderr, code := run(c.MkDir(), "help", "plugins")
	c.Assert(code, gc.Equals, 0)
	c.Assert(strings.HasPrefix(stdout, charmcmd.PluginTopicText), jc.IsTrue)
	return stdout[len(charmcmd.PluginTopicText):], stderr
}
