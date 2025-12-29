## Corndogs Protos

This directory contains the Protocol Buffer definitions for the Corndogs task queue system.

The protos are now maintained in the unified [catalystcommunity/corndogs](https://github.com/catalystcommunity/corndogs) repository.

### Historical Changelog (from TnLCommunity/protos-corndogs)

## [1.2.1](https://github.com/catalystcommunity/corndogs/compare/v1.2.0...v1.2.1) (2023-01-03)


### Bug Fixes

* corndogs metrics protos
* dont use EmptyRequest

# [1.2.0](https://github.com/catalystcommunity/corndogs/compare/v1.1.3...v1.2.0) (2022-12-28)


### Features

* add priority fields

## [1.1.3](https://github.com/catalystcommunity/corndogs/compare/v1.1.2...v1.1.3) (2022-12-16)


### Bug Fixes

* add a queue field to CleanUpTimedOutRequest

## [1.1.2](https://github.com/catalystcommunity/corndogs/compare/v1.1.1...v1.1.2) (2022-12-06)


### Bug Fixes

* add an auto_target_state to GetNextTaskRequest
* use override prefix, add override_current_state

## [1.1.1](https://github.com/catalystcommunity/corndogs/compare/v1.1.0...v1.1.1) (2022-12-04)


### Bug Fixes

* add response for CleanUpTimedOut and append request suffix
* add service endpoint, fix name of response

# [1.1.0](https://github.com/catalystcommunity/corndogs/compare/v1.0.2...v1.1.0) (2022-12-01)


### Bug Fixes

* add back an updated buf.lock
* remove buf.lock since it references a nonexistant commit
* switch to catalystsquad action-buf since its more up to date


### Features

* add CleanUpTimedOut message

## [1.0.2](https://github.com/catalystcommunity/corndogs/compare/v1.0.1...v1.0.2) (2022-12-01)


### Bug Fixes

* generate new lock file
* switch to catalystsquad action-buf since its more up to date
* use catalystsquad action-buf, use git runner token

## [1.0.1](https://github.com/catalystcommunity/corndogs/compare/v1.0.0...v1.0.1) (2022-02-06)


### Bug Fixes

* add name to buf.yaml

# 1.0.0 (2022-02-06)


### Bug Fixes

* add comment to trigger actions
