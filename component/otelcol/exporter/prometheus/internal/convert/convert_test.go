package convert_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/grafana/agent/component/otelcol/exporter/prometheus/internal/convert"
	"github.com/grafana/agent/pkg/util"
	"github.com/grafana/agent/pkg/util/testappender"
	"github.com/prometheus/prometheus/storage"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestConverter(t *testing.T) {
	tt := []struct {
		name   string
		input  string
		expect string

		showTimestamps     bool
		includeTargetInfo  bool
		includeScopeInfo   bool
		includeScopeLabels bool
		addMetricSuffixes  bool
		enableOpenMetrics  bool
	}{
		{
			name: "Gauge",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Monotonic sum",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds_total",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": true,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15,
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds counter
				test_metric_seconds_total 15.0 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Non-monotonic sum",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": false,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds 15.0
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Histogram",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"bucket_counts": [0, 111, 0, 222],
									"explicit_bounds": [0.25, 0.5, 0.75, 1.0],
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										},
										{
											"time_unix_nano": 1000000003,
											"as_double": 1.5,
											"span_id": "cccccccccccccccc",
											"trace_id": "cccccccccccccccccccccccccccccccc"
										},
										{
											"time_unix_nano": 1000000002,
											"as_double": 0.5,
											"span_id": "bbbbbbbbbbbbbbbb",
											"trace_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds histogram
				test_metric_seconds_bucket{le="0.25"} 0
				test_metric_seconds_bucket{le="0.5"} 111 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
				test_metric_seconds_bucket{le="0.75"} 111 # {span_id="bbbbbbbbbbbbbbbb",trace_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} 0.5
				test_metric_seconds_bucket{le="1.0"} 333
				test_metric_seconds_bucket{le="+Inf"} 333 # {span_id="cccccccccccccccc",trace_id="cccccccccccccccccccccccccccccccc"} 1.5
				test_metric_seconds_sum 100.0
				test_metric_seconds_count 333
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Histogram out-of-order bounds",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"bucket_counts": [0, 111, 0, 222],
									"explicit_bounds": [0.5, 1.0, 0.25, 0.75]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds histogram
				test_metric_seconds_bucket{le="0.25"} 0
				test_metric_seconds_bucket{le="0.5"} 0
				test_metric_seconds_bucket{le="0.75"} 222
				test_metric_seconds_bucket{le="1.0"} 333
				test_metric_seconds_bucket{le="+Inf"} 333
				test_metric_seconds_sum 100.0
				test_metric_seconds_count 333
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Exponential Histogram",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_exponential_histogram",
							"exponential_histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"scale": 0,
									"count": 11,
									"sum": 158.63,
									"positive": {
										"offset": -1,
										"bucket_counts": [2, 1, 3, 2, 0, 0, 3]
									},
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 3.0,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										},
										{
											"time_unix_nano": 1000000003,
											"as_double": 1.0,
											"span_id": "cccccccccccccccc",
											"trace_id": "cccccccccccccccccccccccccccccccc"
										},
										{
											"time_unix_nano": 1000000002,
											"as_double": 1.5,
											"span_id": "bbbbbbbbbbbbbbbb",
											"trace_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			// Native histograms don't have text format yet so we compare the string representation.
			// The tests only allow one exemplar/series because it uses a map[series]exemplar as storage. Therefore only the exemplar "bbbbbbbbbbbbbbbb" is stored.
			expect:            `sample_count:11 sample_sum:158.63 bucket:{exemplar:{label:{name:"span_id" value:"bbbbbbbbbbbbbbbb"} label:{name:"trace_id" value:"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} value:1.5}} schema:0 zero_threshold:1e-128 zero_count:0 positive_span:{offset:0 length:7} positive_delta:2 positive_delta:-1 positive_delta:2 positive_delta:-1 positive_delta:-2 positive_delta:0 positive_delta:3`,
			enableOpenMetrics: true,
		},
		{
			name: "Exponential Histogram 2",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_exponential_histogram_2",
							"exponential_histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"scale": 2,
									"count": 19,
									"sum": 200,
									"zero_count" : 5,
									"zero_threshold": 0.1,
									"positive": {
										"offset": 3,
										"bucket_counts": [0, 0, 0, 0, 2, 1, 1, 0, 3, 0, 0]
									},
									"negative": {
										"offset": 0,
										"bucket_counts": [0, 4, 0, 2, 3, 0, 0, 3]
									},
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 3.0,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			// Native histograms don't have text format yet so we compare the string representation.
			// zero_threshold is set to 1e-128 because dp.ZeroThreshold() is not yet available.
			expect:            `sample_count:19 sample_sum:200 bucket:{exemplar:{label:{name:"span_id" value:"aaaaaaaaaaaaaaaa"} label:{name:"trace_id" value:"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} value:3}} schema:2 zero_threshold:1e-128 zero_count:5 negative_span:{offset:1 length:8} negative_delta:0 negative_delta:4 negative_delta:-4 negative_delta:2 negative_delta:1 negative_delta:-3 negative_delta:0 negative_delta:3 positive_span:{offset:4 length:0} positive_span:{offset:4 length:7} positive_delta:2 positive_delta:-1 positive_delta:0 positive_delta:-1 positive_delta:3 positive_delta:-3 positive_delta:0`,
			enableOpenMetrics: true,
		},
		{
			name: "Summary",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"summary": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"quantile_values": [
										{ "quantile": 0, "value": 100 },
										{ "quantile": 0.25, "value": 200 },
										{ "quantile": 0.5, "value": 300 },
										{ "quantile": 0.75, "value": 400 },
										{ "quantile": 1, "value": 500 }
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds summary
				test_metric_seconds{quantile="0.0"} 100.0
				test_metric_seconds{quantile="0.25"} 200.0
				test_metric_seconds{quantile="0.5"} 300.0
				test_metric_seconds{quantile="0.75"} 400.0
				test_metric_seconds{quantile="1.0"} 500.0
				test_metric_seconds_sum 100.0
				test_metric_seconds_count 333
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Timestamps",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			showTimestamps: true,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds 1234.56 1.0
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Labels from resource attributes",
			input: `{
				"resource_metrics": [{
					"resource": {
						"attributes": [{
							"key": "service.name",
							"value": { "stringValue": "myservice" }
						}, {
							"key": "service.instance.id",
							"value": { "stringValue": "instance" }
						}, {
							"key": "do_not_display",
							"value": { "stringValue": "test" }
						}]
					},
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds{instance="instance",job="myservice"} 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Labels from scope name and version",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"scope": {
							"name": "a-name",
							"version": "a-version",
							"attributes": [{
								"key": "something.extra",
								"value": { "stringValue": "zzz-extra-value" }
							}]
						},
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			includeScopeInfo: true,
			expect: `
				# TYPE otel_scope_info gauge
				otel_scope_info{otel_scope_name="a-name",otel_scope_version="a-version",something_extra="zzz-extra-value"} 1.0
				# TYPE test_metric_seconds gauge
				test_metric_seconds 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Labels from data point",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"scope": {
							"name": "a-name",
							"version": "a-version",
							"attributes": [{
								"key": "something.extra",
								"value": { "stringValue": "zzz-extra-value" }
							}]
						},
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"attributes": [{
										"key": "foo",
										"value": { "stringValue": "bar" }
									}],
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			includeScopeLabels: true,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds{otel_scope_name="a-name",otel_scope_version="a-version",foo="bar"} 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Target info metric",
			input: `{
				"resource_metrics": [{
					"resource": {
						"attributes": [{
							"key": "service.name",
							"value": { "stringValue": "myservice" }
						}, {
							"key": "service.instance.id",
							"value": { "stringValue": "instance" }
						}, {
							"key": "custom_attr",
							"value": { "stringValue": "test" }
						}]
					},
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_seconds",
							"gauge": {
								"data_points": [{
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			includeTargetInfo: true,
			expect: `
				# HELP target_info Target metadata
				# TYPE target_info gauge
				target_info{instance="instance",job="myservice",custom_attr="test"} 1.0
				# TYPE test_metric_seconds gauge
				test_metric_seconds{instance="instance",job="myservice"} 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Gauge: add_metric_suffixes = false",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"gauge": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric gauge
				test_metric 1234.56
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Gauge: add_metric_suffixes = true",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"gauge": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 1234.56
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds 1234.56
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: true,
		},
		{
			name: "Monotonic sum: add_metric_suffixes = false",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_total",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": true,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15,
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric counter
				test_metric_total 15.0 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Monotonic sum: add_metric_suffixes = true",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric_total",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": true,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15,
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds counter
				test_metric_seconds_total 15.0 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: true,
		},
		{
			name: "Monotonic sum: add_metric_suffixes = false, don't convert to open metrics",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": true,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15,
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric counter
				test_metric 15
			`,
			enableOpenMetrics: false,
		},
		{
			name: "Monotonic sum: add_metric_suffixes = true, don't convert to open metrics",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": true,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15,
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds_total counter
				test_metric_seconds_total 15
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: false,
		},
		{
			name: "Non-monotonic sum: add_metric_suffixes = false",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": false,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric gauge
				test_metric 15.0
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Non-monotonic sum: add_metric_suffixes = true",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"sum": {
								"aggregation_temporality": 2,
								"is_monotonic": false,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"as_double": 15
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds gauge
				test_metric_seconds 15.0
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: true,
		},
		{
			name: "Histogram: add_metric_suffixes = false",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"bucket_counts": [0, 111, 0, 222],
									"explicit_bounds": [0.25, 0.5, 0.75, 1.0],
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										},
										{
											"time_unix_nano": 1000000003,
											"as_double": 1.5,
											"span_id": "cccccccccccccccc",
											"trace_id": "cccccccccccccccccccccccccccccccc"
										},
										{
											"time_unix_nano": 1000000002,
											"as_double": 0.5,
											"span_id": "bbbbbbbbbbbbbbbb",
											"trace_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric histogram
				test_metric_bucket{le="0.25"} 0
				test_metric_bucket{le="0.5"} 111 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
				test_metric_bucket{le="0.75"} 111 # {span_id="bbbbbbbbbbbbbbbb",trace_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} 0.5
				test_metric_bucket{le="1.0"} 333
				test_metric_bucket{le="+Inf"} 333 # {span_id="cccccccccccccccc",trace_id="cccccccccccccccccccccccccccccccc"} 1.5
				test_metric_sum 100.0
				test_metric_count 333
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Histogram: add_metric_suffixes = true",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"histogram": {
								"aggregation_temporality": 2,
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"bucket_counts": [0, 111, 0, 222],
									"explicit_bounds": [0.25, 0.5, 0.75, 1.0],
									"exemplars":[
										{
											"time_unix_nano": 1000000001,
											"as_double": 0.3,
											"span_id": "aaaaaaaaaaaaaaaa",
											"trace_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
										},
										{
											"time_unix_nano": 1000000003,
											"as_double": 1.5,
											"span_id": "cccccccccccccccc",
											"trace_id": "cccccccccccccccccccccccccccccccc"
										},
										{
											"time_unix_nano": 1000000002,
											"as_double": 0.5,
											"span_id": "bbbbbbbbbbbbbbbb",
											"trace_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
										}
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds histogram
				test_metric_seconds_bucket{le="0.25"} 0
				test_metric_seconds_bucket{le="0.5"} 111 # {span_id="aaaaaaaaaaaaaaaa",trace_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} 0.3
				test_metric_seconds_bucket{le="0.75"} 111 # {span_id="bbbbbbbbbbbbbbbb",trace_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} 0.5
				test_metric_seconds_bucket{le="1.0"} 333
				test_metric_seconds_bucket{le="+Inf"} 333 # {span_id="cccccccccccccccc",trace_id="cccccccccccccccccccccccccccccccc"} 1.5
				test_metric_seconds_sum 100.0
				test_metric_seconds_count 333
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: true,
		},
		{
			name: "Summary: add_metric_suffixes = false",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"summary": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"quantile_values": [
										{ "quantile": 0, "value": 100 },
										{ "quantile": 0.25, "value": 200 },
										{ "quantile": 0.5, "value": 300 },
										{ "quantile": 0.75, "value": 400 },
										{ "quantile": 1, "value": 500 }
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric summary
				test_metric{quantile="0.0"} 100.0
				test_metric{quantile="0.25"} 200.0
				test_metric{quantile="0.5"} 300.0
				test_metric{quantile="0.75"} 400.0
				test_metric{quantile="1.0"} 500.0
				test_metric_sum 100.0
				test_metric_count 333
			`,
			enableOpenMetrics: true,
		},
		{
			name: "Summary: add_metric_suffixes = true",
			input: `{
				"resource_metrics": [{
					"scope_metrics": [{
						"metrics": [{
							"name": "test_metric",
							"unit": "seconds",
							"summary": {
								"data_points": [{
									"start_time_unix_nano": 1000000000,
									"time_unix_nano": 1000000000,
									"count": 333,
									"sum": 100,
									"quantile_values": [
										{ "quantile": 0, "value": 100 },
										{ "quantile": 0.25, "value": 200 },
										{ "quantile": 0.5, "value": 300 },
										{ "quantile": 0.75, "value": 400 },
										{ "quantile": 1, "value": 500 }
									]
								}]
							}
						}]
					}]
				}]
			}`,
			expect: `
				# TYPE test_metric_seconds summary
				test_metric_seconds{quantile="0.0"} 100.0
				test_metric_seconds{quantile="0.25"} 200.0
				test_metric_seconds{quantile="0.5"} 300.0
				test_metric_seconds{quantile="0.75"} 400.0
				test_metric_seconds{quantile="1.0"} 500.0
				test_metric_seconds_sum 100.0
				test_metric_seconds_count 333
			`,
			addMetricSuffixes: true,
			enableOpenMetrics: true,
		},
	}

	decoder := &pmetric.JSONUnmarshaler{}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := decoder.UnmarshalMetrics([]byte(tc.input))
			require.NoError(t, err)

			var app testappender.Appender
			app.HideTimestamps = !tc.showTimestamps

			l := util.TestLogger(t)
			conv := convert.New(l, appenderAppendable{Inner: &app}, convert.Options{
				IncludeTargetInfo:  tc.includeTargetInfo,
				IncludeScopeInfo:   tc.includeScopeInfo,
				IncludeScopeLabels: tc.includeScopeLabels,
				AddMetricSuffixes:  tc.addMetricSuffixes,
			})
			require.NoError(t, conv.ConsumeMetrics(context.Background(), payload))

			families, err := app.MetricFamilies()
			require.NoError(t, err)

			// TODO: remove this once native histograms have text format.
			// If you add another exponential histogram make sure that the metric name contains "exponential" (see dtobuilder.go)
			if tc.name == "Exponential Histogram" || tc.name == "Exponential Histogram 2" {
				require.NotEmpty(t, families)
				require.NotNil(t, families[0])
				require.NotEmpty(t, families[0].Metric)
				require.NotNil(t, families[0].Metric[0].Histogram)
				require.Equal(t, normalizeSpace(families[0].Metric[0].Histogram.String()), tc.expect)
				return
			}

			c := testappender.Comparer{OpenMetrics: tc.enableOpenMetrics}
			require.NoError(t, c.Compare(families, tc.expect))
		})
	}
}

// appenderAppendable always returns the same Appender.
type appenderAppendable struct {
	Inner storage.Appender
}

var _ storage.Appendable = appenderAppendable{}

func (aa appenderAppendable) Appender(context.Context) storage.Appender {
	return aa.Inner
}

// Temporary function to remove once native histograms have text format
// normalizeSpace takes a string and replaces multiple spaces with a single space,
// and trims leading and trailing spaces.
func normalizeSpace(str string) string {
	space := regexp.MustCompile(`\s+`)
	return strings.TrimSpace(space.ReplaceAllString(str, " "))
}
