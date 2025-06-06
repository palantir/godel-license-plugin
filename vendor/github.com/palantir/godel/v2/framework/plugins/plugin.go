// Copyright 2016 Palantir Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/palantir/godel/v2/framework/artifactresolver"
	"github.com/palantir/godel/v2/framework/godellauncher"
	"github.com/palantir/godel/v2/framework/internal/pathsinternal"
	"github.com/palantir/godel/v2/framework/internal/pluginsinternal"
	"github.com/palantir/godel/v2/framework/pluginapi/v2/pluginapi"
	"github.com/palantir/godel/v2/pkg/osarch"
	"github.com/pkg/errors"
	"github.com/rogpeppe/go-internal/lockedfile"
)

// pluginInfoWithAssets bundles a pluginapi.Info with the locators of all the assets specified for it.
type pluginInfoWithAssets struct {
	PluginInfo pluginapi.PluginInfo
	Assets     []artifactresolver.Locator
}

// LoadPluginsTasks returns the tasks defined by the plugins in the specified parameters. Does the following:
//
//   - Resolves the plugins defined in the provided params for the runtime environment's OS/Architecture into the
//     gödel home plugins and downloads directories.
//   - Verifies that the resolved plugins are valid and compatible with each other (for example, ensures that multiple
//     plugins do not provide the same task).
//   - Creates runnable godellauncher.Task tasks for the plugins.
//
// Returns the tasks provided by the plugins in the provided parameters.
func LoadPluginsTasks(pluginsParam godellauncher.PluginsParam, stdout io.Writer) ([]godellauncher.Task, []godellauncher.UpgradeConfigTask, error) {
	return loadPluginsTasks(pluginsParam, stdout, "")
}

// loadPluginsTasks is a helper function that loads the tasks defined by the plugins in the specified parameters and
// optionally uses a cache for plugin information.
//
// If cachePath is non-empty, it is used as the path to a file that contains the plugin information, which is a
// map[artifactresolver.Locator]pluginInfoWithAssets. If the cache file exists, it is read and used to load the plugin
// information. If the cache file does not exist, the work to resolve the plugins and verify their validity is performed
// and then the resulting plugin information is written to the cache file.
func loadPluginsTasks(pluginsParam godellauncher.PluginsParam, stdout io.Writer, cachePath string) ([]godellauncher.Task, []godellauncher.UpgradeConfigTask, error) {
	pluginsDir, assetsDir, downloadsDir, err := pathsinternal.ResourceDirs()
	if err != nil {
		return nil, nil, err
	}

	var plugins map[artifactresolver.Locator]pluginInfoWithAssets
	if cachePath != "" {
		if pluginsConfigCacheBytes, err := os.ReadFile(cachePath); err == nil {
			loadedPlugins, err := unmarshalPluginsInfoJSON(pluginsConfigCacheBytes)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "failed to unmarshal plugin information")
			}
			plugins = loadedPlugins
		} else if !os.IsNotExist(err) {
			return nil, nil, errors.Wrapf(err, "failed to read plugin information from cache file at %q", cachePath)
		}
	}

	if plugins == nil {
		plugins, err = resolvePlugins(pluginsDir, assetsDir, downloadsDir, osarch.Current(), pluginsParam, stdout)
		if err != nil {
			return nil, nil, err
		}
		if err := verifyPluginCompatibility(plugins); err != nil {
			return nil, nil, err
		}

		if cachePath != "" {
			// write plugin information to cache file
			pluginsJSON, err := marshalPluginsInfoJSON(plugins)
			if err != nil {
				return nil, nil, errors.Wrapf(err, "failed to marshal plugin information")
			}
			if err := writeFileUsingRename(cachePath, pluginsJSON); err != nil {
				return nil, nil, errors.Wrapf(err, "failed to write plugin information to cache file at %q", cachePath)
			}
		}
	}

	var sortedPluginLocators []artifactresolver.Locator
	for k := range plugins {
		sortedPluginLocators = append(sortedPluginLocators, k)
	}
	pluginsinternal.SortLocators(sortedPluginLocators)

	var tasks []godellauncher.Task
	var upgradeConfigTasks []godellauncher.UpgradeConfigTask
	for _, pluginLoc := range sortedPluginLocators {
		pluginExecPath := pathsinternal.PluginPath(pluginsDir, pluginLoc)
		pluginInfoWithAssets := plugins[pluginLoc]

		var assetPaths []string
		for _, assetLoc := range pluginInfoWithAssets.Assets {
			assetPaths = append(assetPaths, pathsinternal.PluginPath(assetsDir, assetLoc))
		}
		tasks = append(tasks, pluginInfoWithAssets.PluginInfo.Tasks(pluginExecPath, assetPaths)...)

		upgradeConfigTask := pluginInfoWithAssets.PluginInfo.UpgradeConfigTask(pluginExecPath, assetPaths)
		if upgradeConfigTask != nil {
			upgradeConfigTasks = append(upgradeConfigTasks, *upgradeConfigTask)
		}
	}
	return tasks, upgradeConfigTasks, nil
}

// resolvePlugins resolves all of the plugins defined in the provided params for the specified osArch using the provided
// plugins and downloads directories. Returns a map that contains all of the information for the valid plugins. If
// errors were encountered while trying to resolve plugins, returns an error that summarizes the errors. This function
// locks the "godel-resolver.lock" file in the plugins directory (creating it if it does not exist) for the duration of
// the function, which ensures that only one instance of this function will run at a given time (both within and across
// godel processes) and eliminates race conditions around different godel processes resolving plugins concurrently.
//
// For each plugin defined in the parameters:
//
// * If a file does not exist in the expected location in the plugins directory, resolve it
//   - If the configuration specifies a custom resolver for the plugin, use it to resolve the plugin TGZ into the
//     downloads directory
//   - Otherwise, if default resolvers are specified in the parameters, try to resolve the plugin TGZ into the
//     downloads directory from each of them in order
//   - If the plugin TGZ cannot be resolved, return an error
//   - If the plugin TGZ was resolved, unpack the content of the TGZ (which must contain a single file) into the
//     expected location in the plugins directory
//   - If the configuration specifies a checksum for the plugin and the specified osArch, verify that the checksum of
//     the plugin in the plugins directory matches the specified checksum
//   - Invoke the plugin info command (specified by the InfoCommandName constant) on the plugin and parse the output
//     as the plugin information
//   - If the plugin specifies assets, resolve all of the assets
//   - Asset resolution uses a process that is analogous to plugin resolution, but performs it in the assets directory
func resolvePlugins(pluginsDir, assetsDir, downloadsDir string, osArch osarch.OSArch, pluginsParam godellauncher.PluginsParam, stdout io.Writer) (map[artifactresolver.Locator]pluginInfoWithAssets, error) {
	// lock global resolver file while performing resolution
	pluginResolverLockFilePath := filepath.Join(pluginsDir, "godel-resolver.lock")
	pluginResolverLock := lockedfile.MutexAt(pluginResolverLockFilePath)
	unlock, err := pluginResolverLock.Lock()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to lock resolver mutex file")
	}
	defer unlock()

	plugins := make(map[artifactresolver.Locator]pluginInfoWithAssets)
	pluginErrors := make(map[artifactresolver.Locator]error)
	for _, currPlugin := range pluginsParam.Plugins {
		currPluginLocator, ok := pluginsinternal.ResolveAndVerify(
			currPlugin.LocatorWithResolverParam,
			pluginErrors,
			pluginsDir,
			downloadsDir,
			pluginsParam.DefaultResolvers,
			osArch,
			stdout,
		)
		if !ok {
			continue
		}
		info, err := pluginapi.InfoFromPlugin(filepath.Join(pluginsDir, pathsinternal.PluginFileName(currPluginLocator)))
		if err != nil {
			pluginErrors[currPluginLocator] = errors.Wrapf(err, "failed to get plugin info for plugin %+v", currPluginLocator)
			continue
		}

		// plugin has been successfully resolved: resolve assets for plugin
		assetInfoMap, err := pluginsinternal.ResolveAssets(assetsDir, downloadsDir, currPlugin.Assets, osArch, pluginsParam.DefaultResolvers, stdout)
		if err != nil {
			pluginErrors[currPluginLocator] = errors.Wrapf(err, "failed to get asset(s) for plugin %+v", currPluginLocator)
			continue
		}

		plugins[currPluginLocator] = pluginInfoWithAssets{
			PluginInfo: info,
			Assets:     assetInfoMap,
		}
	}

	if len(pluginErrors) == 0 {
		return plugins, nil
	}

	// encountered errors: summarize and return
	var sortedKeys []artifactresolver.Locator
	for k := range pluginErrors {
		sortedKeys = append(sortedKeys, k)
	}
	pluginsinternal.SortLocators(sortedKeys)

	errStringsParts := []string{fmt.Sprintf("failed to resolve %d plugin(s):", len(pluginErrors))}
	for _, k := range sortedKeys {
		errStringsParts = append(errStringsParts, pluginErrors[k].Error())
	}
	return nil, errors.New(strings.Join(errStringsParts, "\n"+strings.Repeat(" ", pluginsinternal.IndentSpaces)))
}

// Verifies that the plugins in the provided map are compatible with one another. Specifically, ensures that:
//   - There is at most 1 version of a given plugin (a locator with a given {group, product} pair)
//   - There are no conflicts between tasks provided by the plugins
//   - There are no 2 plugins that use a configuration file that have the same plugin name
func verifyPluginCompatibility(plugins map[artifactresolver.Locator]pluginInfoWithAssets) error {
	// map from a plugin locator to the locators to all of the plugins that they conflict with and the error that
	// describes the conflict.
	conflicts := make(map[artifactresolver.Locator]map[artifactresolver.Locator]error)
	for currPlugin := range plugins {
		currConflicts := verifySinglePluginCompatibility(currPlugin, plugins)
		if len(currConflicts) == 0 {
			continue
		}
		conflicts[currPlugin] = currConflicts
	}

	if len(conflicts) == 0 {
		return nil
	}

	var sortedOuterKeys []artifactresolver.Locator
	for k := range conflicts {
		sortedOuterKeys = append(sortedOuterKeys, k)
	}
	pluginsinternal.SortLocators(sortedOuterKeys)

	errString := fmt.Sprintf("%d plugins had compatibility issues:", len(conflicts))
	for _, k := range sortedOuterKeys {
		errString += fmt.Sprintf("\n%s%s:", strings.Repeat(" ", pluginsinternal.IndentSpaces), k.String())

		var sortedInnerKeys []artifactresolver.Locator
		for innerK := range conflicts[k] {
			sortedInnerKeys = append(sortedInnerKeys, innerK)
		}
		pluginsinternal.SortLocators(sortedInnerKeys)

		for _, innerK := range sortedInnerKeys {
			errString += fmt.Sprintf("\n%s%s", strings.Repeat(" ", pluginsinternal.IndentSpaces*2), conflicts[k][innerK].Error())
		}
	}
	return errors.New(errString)
}

func verifySinglePluginCompatibility(plugin artifactresolver.Locator, plugins map[artifactresolver.Locator]pluginInfoWithAssets) map[artifactresolver.Locator]error {
	errs := make(map[artifactresolver.Locator]error)
	for otherPlugin, otherPluginInfo := range plugins {
		if otherPlugin == plugin {
			continue
		}
		if otherPlugin.Group == plugin.Group && otherPlugin.Product == plugin.Product {
			errs[otherPlugin] = fmt.Errorf("different version of the same plugin")
			continue
		}

		if plugin.Product == otherPlugin.Product {
			// if product names are the same, verify that they do not both use configuration (if they do, the
			// configuration files will conflict)
			if plugins[plugin].PluginInfo.UsesConfig() && otherPluginInfo.PluginInfo.UsesConfig() {
				errs[otherPlugin] = fmt.Errorf("plugins have the same product name and both use configuration (this not currently supported -- if this situation is encountered, please file an issue to flag it)")
				continue
			}
		}

		currPluginInfo := plugins[plugin]
		var currPluginTasks []string
		for _, currPluginTask := range currPluginInfo.PluginInfo.Tasks("", nil) {
			currPluginTasks = append(currPluginTasks, currPluginTask.Name)
		}
		sort.Strings(currPluginTasks)

		otherPluginTasks := make(map[string]struct{})
		for _, task := range otherPluginInfo.PluginInfo.Tasks("", nil) {
			otherPluginTasks[task.Name] = struct{}{}
		}

		var commonTasks []string
		for _, currPluginTask := range currPluginTasks {
			if _, ok := otherPluginTasks[currPluginTask]; !ok {
				continue
			}
			commonTasks = append(commonTasks, currPluginTask)
		}
		if len(commonTasks) != 0 {
			errs[otherPlugin] = fmt.Errorf("provides conflicting tasks: %v", commonTasks)
			continue
		}
	}
	return errs
}
