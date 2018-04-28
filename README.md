godel-license-plugin
====================
`godel-license-plugin` is a [godel](https://github.com/palantir/godel) plugin for [`go-license`](https://github.com/palantir/go-license). It provides a task that can add, remove and verify license headers on project files. 

Tasks
-----
* `license`: adds licenses to files based on configuration. 

Verify
------
When run as part of the `verify` task, if `apply=true`, then the `verify` task is run. If `apply=false`, then `license --verify` is run, which verifies that all of the files in the repository that match the configuration have the correct license headers as specified by the configuration. 
