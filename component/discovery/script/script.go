package script

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/grafana/agent/component"
	"github.com/grafana/agent/component/discovery"
	"github.com/grafana/agent/pkg/flow/logging/level"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

func init() {
	component.Register(component.Registration{
		Name:    "discovery.script",
		Args:    Arguments{},
		Exports: Exports{},

		Build: func(opts component.Options, args component.Arguments) (component.Component, error) {
			return New(opts, args.(Arguments))
		},
	})
}

// Arguments holds values which are used to configure the discovery.script component.
type Arguments struct {
	// Targets contains the input 'targets' passed by a service discovery component.
	Targets []discovery.Target `river:"targets,attr"`
	// Script contains a Python (Starlark dialect) script to run that will relabel the targets.
	// The script must contain a function with the signature: `def relabel_targets(targets)` that takes a list of
	// dictionaries representing the targets and returns a new list of dictionaries for the user-modified targets.
	Script string `river:"script,attr"`
}

// Exports holds values which are exported by the discovery.script component.
type Exports struct {
	Output []discovery.Target `river:"output,attr"`
}

// Component implements the discovery.script component.
type Component struct {
	opts component.Options

	mut              sync.RWMutex
	currentScript    string
	thread           *starlark.Thread
	relabelTargetsFn starlark.Value
}

var _ component.Component = (*Component)(nil)

// New creates a new discovery.script component.
func New(o component.Options, args Arguments) (*Component, error) {
	c := &Component{opts: o}

	// Call to Update() to set the output once at the start
	if err := c.Update(args); err != nil {
		return nil, err
	}

	return c, nil
}

// Run implements component.Component.
func (c *Component) Run(ctx context.Context) error {
	<-ctx.Done()
	c.thread.Cancel("component exiting")
	return nil
}

// Update implements component.Component.
func (c *Component) Update(args component.Arguments) error {
	c.mut.Lock()
	defer c.mut.Unlock()

	newArgs := args.(Arguments)

	if err := c.updateScript(newArgs.Script); err != nil {
		return err
	}

	targets, err := c.doRelabel(newArgs.Targets)
	if err != nil {
		return err
	}

	c.opts.OnStateChange(Exports{
		Output: targets,
	})

	return nil
}

func (c *Component) doRelabel(targets []discovery.Target) ([]discovery.Target, error) {
	sTargets, err := c.toStarlarkTargets(targets)
	if err != nil {
		return nil, fmt.Errorf("error converting targets to Starlark: %w", err)
	}

	value, err := starlark.Call(c.thread, c.relabelTargetsFn, starlark.Tuple{starlark.NewList(sTargets)}, nil)
	if err != nil {
		finalErr := err
		if evalError, ok := err.(*starlark.EvalError); ok {
			finalErr = fmt.Errorf("error calling relabel_targets function in script: %w\n%v\nscript:%v", err, evalError.Backtrace(), numberLines(c.currentScript))
		}
		return nil, finalErr
	}

	return c.toFlowTargets(value)
}

func (c *Component) updateScript(newScript string) error {
	if newScript == c.currentScript {
		return nil
	}
	var opts = syntax.FileOptions{
		Set:             true,
		While:           true,
		TopLevelControl: true,
		Recursion:       true,
	}

	c.thread = &starlark.Thread{Name: c.opts.ID, Load: nil}
	compiled, err := starlark.ExecFileOptions(&opts, c.thread, c.opts.ID, newScript, nil)
	if err != nil {
		return fmt.Errorf("error compiling script: %w\nscript:\n%s", err, numberLines(newScript))
	}

	fn, ok := compiled["relabel_targets"]
	if !ok {
		return fmt.Errorf("script does not contain a relabel_targets function: %s", newScript)
	}
	if sfn, ok := fn.(*starlark.Function); ok {
		if sfn.NumParams() != 1 {
			return fmt.Errorf("the relabel_targets function must accept exactly 1 argument: %s", newScript)
		}
	} else {
		return fmt.Errorf("script must define relabel_targets as a function: %s", newScript)
	}
	c.relabelTargetsFn = fn
	c.currentScript = newScript
	return nil
}

func (c *Component) toFlowTargets(value starlark.Value) ([]discovery.Target, error) {
	listVal, ok := value.(*starlark.List)
	if !ok {
		return nil, fmt.Errorf("relabel_targets function in script did not return "+
			"a list of dictionaries compatible with targets: %+v", value)
	}

	newTargets := make([]discovery.Target, 0, listVal.Len())

	it := listVal.Iterate()
	defer it.Done()

	var val starlark.Value
	for it.Next(&val) {
		dictVal, ok := val.(*starlark.Dict)
		if !ok {
			level.Error(c.opts.Logger).Log("msg", "skipping invalid target: relabel_targets function must return a list of dictionaries", "element", fmt.Sprintf("%+v", val))
			continue
		}
		newTarget := make(discovery.Target)
		for _, k := range dictVal.Keys() {
			v, _, _ := dictVal.Get(k)
			if key, ok := k.(starlark.String); ok {
				newTarget[string(key)] = strings.Trim(v.String(), "\"")
			} else {
				level.Error(c.opts.Logger).Log("msg", "skipping invalid target label: relabel_targets function must return a list of dictionaries with string keys", "key", fmt.Sprintf("%+v", k))
				continue
			}
		}
		if len(newTarget) > 0 {
			newTargets = append(newTargets, newTarget)
		}
	}

	return newTargets, nil
}

func (c *Component) toStarlarkTargets(targets []discovery.Target) ([]starlark.Value, error) {
	sTargets := make([]starlark.Value, len(targets))
	for i, target := range targets {
		st := starlark.NewDict(len(target))
		for k, v := range target {
			err := st.SetKey(starlark.String(k), starlark.String(v))
			if err != nil {
				return nil, err
			}
		}
		sTargets[i] = st
	}
	return sTargets, nil
}

func numberLines(script string) string {
	lines := strings.Split(script, "\n")
	for i := range lines {
		lines[i] = fmt.Sprintf("%d: %s", i+1, lines[i])
	}
	return strings.Join(lines, "\n")
}
