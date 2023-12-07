package script

import (
	"fmt"
	"testing"
	"time"

	"github.com/grafana/agent/component/discovery"
	"github.com/grafana/agent/pkg/flow/componenttest"
	"github.com/grafana/agent/pkg/util"
	"github.com/grafana/river"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/maps"
)

var exampleTargets = []discovery.Target{
	{
		"__address__": "node01:12345",
		"job":         "observability/agent",
		"cluster":     "europe-south-1",
	},
	{
		"__address__": "node02:12345",
		"job":         "observability/loki",
		"cluster":     "europe-south-1",
	},
	{
		"__address__": "node03:12345",
		"job":         "observability/mimir",
		"cluster":     "europe-south-1",
	},
}

func TestScript(t *testing.T) {
	testCases := []struct {
		name           string
		script         string
		targets        []discovery.Target
		expected       []discovery.Target
		expectedRunErr error
	}{
		{
			name: "empty",
			script: `
def relabel_targets(targets):
	return targets
			`,
			targets:  []discovery.Target{},
			expected: []discovery.Target{},
		},
		{
			name:           "broken script",
			script:         `int wrongLanguage() { return 0; }`,
			expectedRunErr: fmt.Errorf("error compiling script"),
		},
		{
			name: "exception in script",
			script: `def relabel_targets(targets):
	return targets[100]
`,
			expectedRunErr: fmt.Errorf("discovery.script.test:2:16: in relabel_targets\n" +
				"Error: index 100 out of range: empty list\n" +
				"script:1: def relabel_targets(targets):"),
		},
		{
			name:           "script missing the relabel_targets function",
			script:         `print("hello world")`,
			expectedRunErr: fmt.Errorf("script does not contain a relabel_targets function"),
		},
		{
			name:           "script relabel_targets function got no args",
			script:         `def relabel_targets(): pass`,
			expectedRunErr: fmt.Errorf("the relabel_targets function must accept exactly 1 argument"),
		},
		{
			name:           "script relabel_targets function got two args",
			script:         `def relabel_targets(a, b): pass`,
			expectedRunErr: fmt.Errorf("the relabel_targets function must accept exactly 1 argument"),
		},
		{
			name:           "script relabel_targets is not a function",
			script:         `relabel_targets = 1`,
			expectedRunErr: fmt.Errorf("script must define relabel_targets as a function"),
		},
		{
			name: "return not a list",
			script: `
def relabel_targets(targets):
	return 1
			`,
			targets:        exampleTargets,
			expectedRunErr: fmt.Errorf("relabel_targets function in script did not return a list of dictionaries"),
		},
		{
			name: "return list of non-dict",
			script: `
def relabel_targets(targets):
	return [1, 2, 3]
			`,
			targets:  exampleTargets,
			expected: []discovery.Target{},
		},
		{
			name: "convert wrong value types to string",
			script: `
def relabel_targets(targets):
	return [{"__address__": 1}, {"__address__": 3.14}, {"__address__": [1, 2, 3]}]
			`,
			targets: exampleTargets,
			expected: []discovery.Target{
				{"__address__": "1"}, {"__address__": "3.14"}, {"__address__": "[1, 2, 3]"},
			},
		},
		{
			name: "ignore keys with wrong type",
			script: `
def relabel_targets(targets):
	return [{"__address__": 1, 123: 1}, {"__address__": 2, 3.14: "hello"}, {"__address__": 3, True: False}]
			`,
			targets: exampleTargets,
			expected: []discovery.Target{
				{"__address__": "1"}, {"__address__": "2"}, {"__address__": "3"},
			},
		},
		{
			name: "pass through",
			script: `
def relabel_targets(targets):
	return targets
			`,
			targets:  exampleTargets,
			expected: exampleTargets,
		},
		{
			name: "add pod and namespace",
			script: `
def relabel_targets(targets):
	for t in targets:
		namespace, pod = t["job"].split("/")
		t["namespace"] = namespace
		t["pod"] = pod
	return targets
			`,
			targets: exampleTargets,
			expected: []discovery.Target{
				{
					"__address__": "node01:12345",
					"job":         "observability/agent",
					"cluster":     "europe-south-1",
					"namespace":   "observability",
					"pod":         "agent",
				},
				{
					"__address__": "node02:12345",
					"job":         "observability/loki",
					"cluster":     "europe-south-1",
					"namespace":   "observability",
					"pod":         "loki",
				},
				{
					"__address__": "node03:12345",
					"job":         "observability/mimir",
					"cluster":     "europe-south-1",
					"namespace":   "observability",
					"pod":         "mimir",
				},
			},
		},
		{
			name: "correlate and join targets demo",
			script: `
def relabel_targets(targets):
    joined = {}
    for t in targets:
        if t["__address__"].startswith("mysql://"):
            key = t["__address__"].split("/")[2]
        else:
            key = t.pop("__address__", None)
            t["__host_address__"] = key

        joined[key] = joined[key] | t if key in joined else t	
    return list(joined.values())
`,
			targets: []discovery.Target{
				{
					"__address__": "mysql://node01/something",
					"job":         "db/mysql",
				},
				{
					"__address__": "node01",
					"size":        "xs",
					"cluster":     "europe-south-1",
				},
				{
					"__address__": "node02",
					"size":        "xxs",
					"cluster":     "europe-middle-1",
				},
				{
					"__address__": "mysql://node02/something",
					"job":         "db/mysql",
				},
			},
			expected: []discovery.Target{
				{
					"__address__":      "mysql://node01/something",
					"__host_address__": "node01",
					"job":              "db/mysql",
					"size":             "xs",
					"cluster":          "europe-south-1",
				},
				{
					"__host_address__": "node02",
					"__address__":      "mysql://node02/something",
					"size":             "xxs",
					"cluster":          "europe-middle-1",
					"job":              "db/mysql",
				},
			},
		},
	}
	t.Parallel()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			inputTargets := deepCopyTargets(tc.targets)

			args := Arguments{
				Targets: inputTargets,
				Script:  tc.script,
			}

			ctrl, err := componenttest.NewControllerFromID(util.TestLogger(t), "discovery.script")
			require.NoError(t, err)

			var (
				runErr  error
				runDone = make(chan struct{})
			)
			go func() {
				runErr = ctrl.Run(componenttest.TestContext(t), args)
				close(runDone)
			}()

			// Check for error if needed
			if tc.expectedRunErr != nil {
				<-runDone
				require.Error(t, runErr)
				assert.Contains(t, runErr.Error(), tc.expectedRunErr.Error())
				return
			}

			// Otherwise, verify exports
			require.NoError(t, ctrl.WaitRunning(time.Second), "component never started")
			require.NoError(t, ctrl.WaitExports(time.Second), "component never exported anything")

			require.EventuallyWithT(t, func(t *assert.CollectT) {
				exports := ctrl.Exports().(Exports)
				assert.NotNil(t, exports)
				assert.Equal(t, tc.targets, inputTargets, "input targets were modified")
				assert.Equal(t, tc.expected, exports.Output, "expected export does not match actual export")
			}, 3*time.Second, 10*time.Millisecond, "component never reached the desired state")
		})
	}
}

func TestScriptWithParsing(t *testing.T) {
	script := `
def relabel_targets(targets):
	for t in targets:
		print('got target: ', t)
	return targets
`
	cfg := `
targets = [
	{
		__address__ = "127.0.0.1:12345",
		namespace = "agent",
		pod = "agent",
	},
	{
		__address__ = "127.0.0.1:8888",
		namespace = "loki",
		pod = "loki",
	},
]
script = ` + "`" + script + "`" + `
	`
	var args Arguments
	require.NoError(t, river.Unmarshal([]byte(cfg), &args))

	ctrl, err := componenttest.NewControllerFromID(util.TestLogger(t), "discovery.script")
	require.NoError(t, err)

	go func() { require.NoError(t, ctrl.Run(componenttest.TestContext(t), args)) }()

	require.NoError(t, ctrl.WaitRunning(time.Second), "component never started")
	require.NoError(t, ctrl.WaitExports(time.Second), "component never exported anything")

	require.EventuallyWithT(t, func(t *assert.CollectT) {
		exports := ctrl.Exports().(Exports)
		assert.NotNil(t, exports)
		assert.Contains(t, exports.Output, discovery.Target{
			"__address__": "127.0.0.1:12345",
			"namespace":   "agent",
			"pod":         "agent",
		})
		assert.Contains(t, exports.Output, discovery.Target{
			"__address__": "127.0.0.1:8888",
			"namespace":   "loki",
			"pod":         "loki",
		})
	}, 3*time.Second, 10*time.Millisecond, "component never reached the desired state")
}

func deepCopyTargets(targets []discovery.Target) []discovery.Target {
	inputCopy := make([]discovery.Target, len(targets))
	for i, tg := range targets {
		tCopy := make(discovery.Target, len(tg))
		maps.Copy(tCopy, tg)
		inputCopy[i] = tCopy
	}
	return inputCopy
}
