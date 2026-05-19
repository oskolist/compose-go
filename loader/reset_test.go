/*
   Copyright 2020 The Compose Specification Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package loader

import (
	"context"
	"testing"

	"github.com/oskolist/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

func TestResetRemove(t *testing.T) {
	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{
				Filename: "(inline)",
				Content: []byte(`
name: test-reset
networks:
  test:
    name: test
    external: true
`),
			},
			{
				Filename: "(override)",
				Content: []byte(`
networks:
  test: !reset {}
`),
			},
		},
	}, func(options *Options) {
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	})
	assert.NilError(t, err)
	_, ok := p.Networks["test"]
	assert.Check(t, !ok)
}

func TestOverrideReplace(t *testing.T) {
	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{
				Filename: "(inline)",
				Content: []byte(`
name: test-override
networks:
  test:
    name: test
    external: true
`),
			},
			{
				Filename: "(override)",
				Content: []byte(`
networks:
  test: !override {}
`),
			},
		},
	}, func(options *Options) {
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	})
	assert.NilError(t, err)
	assert.Check(t, p.Networks["test"].External == false)
}

func TestResetCycle(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		expectError bool
		errorMsg    string
	}{
		{
			name: "simple_alias_no_cycle",
			config: `
name: test
services:
  a: &a
    image: alpine
  a2: *a
`,
			expectError: false,
		},
		{
			name: "simple_alias_reversed_no_cycle",
			config: `
name: test
services:
  a2: &a
    image: alpine
  a: *a
`,
			expectError: false,
		},
		{
			name: "nested_merge_no_cycle",
			config: `
name: test
x-templates:
  x-gluetun: &gluetun
    environment: &gluetun_env
      a: b
  x-gluetun-pia: &gluetun_pia
    <<: *gluetun
  x-gluetun-env-pia: &gluetun_env_pia
    <<: *gluetun_env
  vp0:
    <<: *gluetun_pia
    environment:
      <<: *gluetun_env_pia
`,
			expectError: false,
		},
		{
			name: "multiple_services_common_config",
			config: `
name: test
x-common:
  &common
  restart: unless-stopped

services:
  backend:
    <<: *common
    image: alpine:latest

  backend-static:
    <<: *common
    image: alpine:latest

  backend-worker:
    <<: *common
    image: alpine:latest
`,
			expectError: false,
		},
		{
			name: "direct_self_reference_cycle",
			config: `
name: test
x-healthcheck: &healthcheck
  egress-service:
    <<: *healthcheck
`,
			expectError: true,
			errorMsg:    "cycle detected: node at path x-healthcheck.egress-service.egress-service references node at path x-healthcheck.egress-service",
		},
	}

	for _, tt := range tests {
		t.Run(
			tt.name, func(t *testing.T) {
				_, err := LoadWithContext(context.TODO(),
					types.ConfigDetails{
						ConfigFiles: []types.ConfigFile{
							{
								Filename: "(inline)",
								Content:  []byte(tt.config),
							},
						},
					}, func(options *Options) {
						options.SkipNormalization = true
						options.SkipConsistencyCheck = true
					},
				)

				if tt.expectError {
					assert.ErrorContains(t, err, tt.errorMsg)
				} else {
					assert.NilError(t, err)
				}
			},
		)
	}
}

func TestResetSequenceItem(t *testing.T) {
	tests := []struct {
		name	 string
		base	 string
		override string
		expected map[string]interface{}
	}{
		{
			name: "reset_sequence_by_name",
			base: `
name: test-reset-seq
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
        host_ip: 127.0.0.1
        published: "8080"
        protocol: tcp
        mode: host
      - name: ssh
        target: 22
        host_ip: 127.0.0.1
        published: "8022"
        protocol: tcp
        mode: host
`,
			override: `
services:
  myservice:
    ports:
      - !reset
        name: ssh
`,
		},
		{
			name: "reset_sequence_by_multiple_fields",
			base: `
name: test-reset-seq-multi
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
        protocol: tcp
      - name: web
        target: 443
        protocol: tcp
      - name: ssh
        target: 22
        protocol: tcp
`,
			override: `
services:
  myservice:
    ports:
      - !reset
        name: web
        target: 443
`,
		},
		{
			name: "reset_sequence_no_match",
			base: `
name: test-reset-seq-nomatch
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
      - name: ssh
        target: 22
`,
			override: `
services:
  myservice:
    ports:
      - !reset
        name: ftp
`,
		},
		{
			name: "reset_sequence_empty_reset",
			base: `
name: test-reset-seq-empty
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
      - name: ssh
        target: 22
`,
			override: `
services:
  myservice:
    ports: !reset []
`,
		},
		{
			name: "reset_sequence_by_values",
			base: `
name: test-reset-seq-values
services:
  myservice:
    image: nginx
    environment:
      - FOO=bar
      - BAR=baz
      - BAZ=qux
`,
			override: `
services:
  myservice:
    environment:
      - !reset
        FOO: bar
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
				ConfigFiles: []types.ConfigFile{
					{
						Filename: "(inline)",
						Content:  []byte(tt.base),
					},
					{
						Filename: "(override)",
						Content:  []byte(tt.override),
					},
				},
			}, func(options *Options) {
				options.SkipNormalization = true
				options.SkipConsistencyCheck = true
			})
			assert.NilError(t, err)

			// Validate based on test case
			switch tt.name {
			case "reset_sequence_by_name":
				ports := p.Services["myservice"].Ports
				assert.Equal(t, len(ports), 1)
				assert.Equal(t, ports[0].Name, "web")
				assert.Equal(t, ports[0].Target, uint32(80))

			case "reset_sequence_by_multiple_fields":
				ports := p.Services["myservice"].Ports
				assert.Equal(t, len(ports), 2)
				// Check that the web:443 port was removed
				for _, port := range ports {
					if port.Name == "web" {
						assert.Equal(t, port.Target, uint32(80))
					}
				}

			case "reset_sequence_no_match":
				ports := p.Services["myservice"].Ports
				assert.Equal(t, len(ports), 2) // Nothing removed

			case "reset_sequence_empty_reset":
				ports := p.Services["myservice"].Ports
				assert.Equal(t, len(ports), 0) // All ports removed

			case "reset_sequence_by_values":
				env := p.Services["myservice"].Environment
				assert.Equal(t, len(env), 2) // Only FOO=bar removed Brock
				_, hasFoo := env["FOO"]
				assert.Check(t, !hasFoo)

			}
		})
	}
}

//func TestResetSequenceWithOverride(t *testing.T) {
//	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
//		ConfigFiles: []types.ConfigFile{
//			{
//				Filename: "(base)",
//				Content: []byte(`
//name: test-reset-override
//services:
//  myservice:
//    image: nginx
//    ports:
//      - name: web
//        target: 80
//        published: "8080"
//      - name: ssh
//        target: 22
//        published: "8022"
//`),
//			},
//			{
//				Filename: "(override)",
//				Content: []byte(`
//services:
//  myservice:
//    ports:
//      - !reset
//        name: ssh
//      - !override
//        name: web
//        target: 8080
//`),
//			},
//		},
//	}, func(options *Options) {
//		options.SkipNormalization = true
//		options.SkipConsistencyCheck = true
//	})
//	assert.NilError(t, err)
//
//	ports := p.Services["myservice"].Ports
//	assert.Equal(t, len(ports), 1)
//	assert.Equal(t, ports[0].Name, "web")
//	assert.Equal(t, ports[0].Target, uint32(8080))
//}

func TestResetSequenceMultipleServices(t *testing.T) {
	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{
				Filename: "(base)",
				Content: []byte(`
name: test-reset-multi-svc
services:
  service1:
    image: nginx
    ports:
      - name: web
        target: 80
      - name: ssh
        target: 22
  service2:
    image: apache
    ports:
      - name: http
        target: 80
      - name: https
        target: 443
`),
			},
			{
				Filename: "(override)",
				Content: []byte(`
services:
  service1:
    ports:
      - !reset
        name: ssh
  service2:
    ports:
      - !reset
        name: https
`),
			},
		},
	}, func(options *Options) {
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	})
	assert.NilError(t, err)

	// Check service1
	ports1 := p.Services["service1"].Ports
	assert.Equal(t, len(ports1), 1)
	assert.Equal(t, ports1[0].Name, "web")

	// Check service2
	ports2 := p.Services["service2"].Ports
	assert.Equal(t, len(ports2), 1)
	assert.Equal(t, ports2[0].Name, "http")
}

func TestResetSequencePartialMatch(t *testing.T) {
	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{
				Filename: "(base)",
				Content: []byte(`
name: test-partial-match
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
        protocol: tcp
        mode: host
      - name: web
        target: 443
        protocol: tcp
        mode: ingress
`),
			},
			{
				Filename: "(override)",
				Content: []byte(`
services:
  myservice:
    ports:
      - !reset
        name: web
        protocol: tcp
`),
			},
		},
	}, func(options *Options) {
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	})
	assert.NilError(t, err)

	// Both ports match the reset criteria, so both should be removed
	ports := p.Services["myservice"].Ports
	assert.Equal(t, len(ports), 0)
}

func TestResetSequenceNoMatchesFound(t *testing.T) {
	p, err := LoadWithContext(context.TODO(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{
				Filename: "(base)",
				Content: []byte(`
name: test-no-matches
services:
  myservice:
    image: nginx
    ports:
      - name: web
        target: 80
`),
			},
			{
				Filename: "(override)",
				Content: []byte(`
services:
  myservice:
    ports:
      - !reset
        name: nonexistent
`),
			},
		},
	}, func(options *Options) {
		options.SkipNormalization = true
		options.SkipConsistencyCheck = true
	})
	assert.NilError(t, err)

	// Nothing should be removed since no port matches
	ports := p.Services["myservice"].Ports
	assert.Equal(t, len(ports), 1)
	assert.Equal(t, ports[0].Name, "web")
}
