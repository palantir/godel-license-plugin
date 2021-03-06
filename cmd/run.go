// Copyright (c) 2018 Palantir Technologies Inc. All rights reserved.
// Use of this source code is governed by the Apache License, Version 2.0
// that can be found in the LICENSE file.

package cmd

import (
	"github.com/palantir/go-license/commoncmd"
	"github.com/palantir/go-license/golicense"
	godelconfig "github.com/palantir/godel/v2/framework/godel/config"
	"github.com/palantir/godel/v2/framework/godellauncher"
	"github.com/palantir/pkg/matcher"
	"github.com/spf13/cobra"
)

var (
	runCmd = &cobra.Command{
		Use: "run",
		RunE: func(cmd *cobra.Command, args []string) error {
			projectCfg, err := commoncmd.LoadConfig(configFlagVal)
			if err != nil {
				return err
			}
			if godelConfigFileFlagVal != "" {
				excludes, err := godelconfig.ReadGodelConfigExcludesFromFile(godelConfigFileFlagVal)
				if err != nil {
					return err
				}
				projectCfg.Exclude.Add(excludes)
			}
			projectParam, err := projectCfg.ToParam()
			if err != nil {
				return err
			}

			// plugin matches all Go files in project except for those excluded by configuration
			goFiles, err := godellauncher.ListProjectPaths(projectDirFlagVal, matcher.Name(`.*\.go`), projectParam.Exclude)
			if err != nil {
				return err
			}
			return golicense.RunLicense(goFiles, projectParam, verifyFlagVal, removeFlagVal, cmd.OutOrStdout())
		},
	}

	verifyFlagVal bool
	removeFlagVal bool
)

func init() {
	runCmd.Flags().BoolVar(&verifyFlagVal, "verify", false, "verify that files have proper license headers applied")
	runCmd.Flags().BoolVar(&removeFlagVal, "remove", false, "remove the license header from files (no-op if verify is true)")
	rootCmd.AddCommand(runCmd)
}
