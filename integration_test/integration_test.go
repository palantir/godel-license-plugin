// Copyright (c) 2018 Palantir Technologies Inc. All rights reserved.
// Use of this source code is governed by the Apache License, Version 2.0
// that can be found in the LICENSE file.

package integration_test

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"testing"
	"time"

	"github.com/palantir/godel/v2/framework/pluginapitester"
	"github.com/palantir/godel/v2/pkg/products"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const godelYML = `exclude:
  names:
    - "\\..+"
    - "vendor"
  paths:
    - "godel"
`

func TestLicense(t *testing.T) {
	pluginPath, err := products.Bin("license-plugin")
	require.NoError(t, err)

	const licenseYML = `header: |
  /*
  Copyright {{YEAR}} Palantir Technologies, Inc.

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
`

	want := fmt.Sprintf(`/*
Copyright %d Palantir Technologies, Inc.

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

package foo`, time.Now().Year())

	projectDir := t.TempDir()
	err = os.MkdirAll(path.Join(projectDir, "godel", "config"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(path.Join(projectDir, "godel", "config", "godel.yml"), []byte(godelYML), 0644)
	require.NoError(t, err)
	err = os.WriteFile(path.Join(projectDir, "godel", "config", "license-plugin.yml"), []byte(licenseYML), 0644)
	require.NoError(t, err)

	writeFiles(t, projectDir, map[string]string{
		"foo.go":                   "package foo",
		"vendor/github.com/bar.go": "package bar",
	})

	outputBuf := &bytes.Buffer{}
	runPluginCleanup, err := pluginapitester.RunPlugin(pluginapitester.NewPluginProvider(pluginPath), nil, "license", nil, projectDir, false, outputBuf)
	defer runPluginCleanup()
	require.NoError(t, err, "Output: %s", outputBuf.String())

	content, err := os.ReadFile(filepath.Join(projectDir, "foo.go"))
	require.NoError(t, err)
	assert.Equal(t, want, string(content))

	want = `package bar`
	content, err = os.ReadFile(filepath.Join(projectDir, "vendor/github.com/bar.go"))
	require.NoError(t, err)
	assert.Equal(t, want, string(content))
}

func TestLicenseVerify(t *testing.T) {
	pluginPath, err := products.Bin("license-plugin")
	require.NoError(t, err)

	const licenseYML = `header: |
  /*
  Copyright {{YEAR}} Palantir Technologies, Inc.

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
`
	projectDir := t.TempDir()
	err = os.MkdirAll(path.Join(projectDir, "godel", "config"), 0755)
	require.NoError(t, err)
	err = os.WriteFile(path.Join(projectDir, "godel", "config", "godel.yml"), []byte(godelYML), 0644)
	require.NoError(t, err)
	err = os.WriteFile(path.Join(projectDir, "godel", "config", "license-plugin.yml"), []byte(licenseYML), 0644)
	require.NoError(t, err)

	writeFiles(t, projectDir, map[string]string{
		"foo.go": "package foo",
		"bar/bar.go": `/*
Copyright 2016 Palantir Technologies, Inc.

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

package bar`,
		"vendor/github.com/baz.go": "package baz",
	})

	outputBuf := &bytes.Buffer{}
	runPluginCleanup, err := pluginapitester.RunPlugin(pluginapitester.NewPluginProvider(pluginPath), nil, "license", []string{
		"--verify",
	}, projectDir, false, outputBuf)
	defer runPluginCleanup()
	require.EqualError(t, err, "")

	wd, err := os.Getwd()
	require.NoError(t, err)

	fooRelPath, err := filepath.Rel(wd, filepath.Join(projectDir, "foo.go"))
	require.NoError(t, err)

	assert.Equal(t, fmt.Sprintf("1 file does not have the correct license header:\n\t%s\n", fooRelPath), outputBuf.String())
}

func TestUpgradeConfig(t *testing.T) {
	pluginPath, err := products.Bin("license-plugin")
	require.NoError(t, err)
	pluginProvider := pluginapitester.NewPluginProvider(pluginPath)

	pluginapitester.RunUpgradeConfigTest(t,
		pluginProvider,
		nil,
		[]pluginapitester.UpgradeConfigTestCase{
			{
				Name: "legacy config is upgraded",
				ConfigFiles: map[string]string{
					"godel/config/license.yml": `
header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.

custom-headers:
  # comment in YAML
  - name: subproject
    header: |
      // Copyright 2016 Palantir Technologies, Inc. All rights reserved.
      // Subproject license.

    paths:
      - subprojectDir
`,
				},
				Legacy: true,
				WantOutput: `Upgraded configuration for license-plugin.yml
`,
				WantFiles: map[string]string{
					"godel/config/license-plugin.yml": `header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.
custom-headers:
- name: subproject
  header: |
    // Copyright 2016 Palantir Technologies, Inc. All rights reserved.
    // Subproject license.
  paths:
  - subprojectDir
`,
				},
			},
			{
				Name: "legacy config is upgraded and empty fields are omitted",
				ConfigFiles: map[string]string{
					"godel/config/license.yml": `
header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.
`,
				},
				Legacy: true,
				WantOutput: `Upgraded configuration for license-plugin.yml
`,
				WantFiles: map[string]string{
					"godel/config/license-plugin.yml": `header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.
`,
				},
			},
			{
				Name: "current config is unmodified",
				ConfigFiles: map[string]string{
					"godel/config/license-plugin.yml": `
header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.

custom-headers:
  # comment in YAML
  - name: subproject
    header: |
      // Copyright 2016 Palantir Technologies, Inc. All rights reserved.
      // Subproject license.

    paths:
      - subprojectDir
`,
				},
				WantOutput: "",
				WantFiles: map[string]string{
					"godel/config/license-plugin.yml": `
header: |
  // Copyright 2016 Palantir Technologies, Inc.
  //
  // License content.

custom-headers:
  # comment in YAML
  - name: subproject
    header: |
      // Copyright 2016 Palantir Technologies, Inc. All rights reserved.
      // Subproject license.

    paths:
      - subprojectDir
`,
				},
			},
		},
	)
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	dir, err := filepath.Abs(root)
	require.NoError(t, err)

	for relPath, content := range files {
		filePath := filepath.Join(dir, relPath)
		err = os.MkdirAll(filepath.Dir(filePath), 0755)
		require.NoError(t, err)
		err = os.WriteFile(filePath, []byte(content), 0644)
		require.NoError(t, err)
	}
}
