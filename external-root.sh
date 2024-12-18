#!/usr/bin/env bash

# This script is the minimalist glue between a bash env and a full reactorcide
# CI/CD flow. The actual behavior should largely not exist here and instead
# this just bootstraps based on assumptions of that system.

set -e

THISSCRIPT=$(basename $0)

SCRIPT_ROOT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

# Config defaults, this section assumes the env you're running in can have
# overrides, but will have defaults where possible. Non-default required items
# are at the top. They are not checked until after sourcing overrides from
# the ${REACTORCIDE_RUNNERENVFILE} var
REACTORCIDE_JOB_REPO_URL="NOTSET"

# Defaulted vals below
REACTORCIDE_RUNNERENVFILE="${REACTORCIDE_RUNNERENVFILE:-runnerenv.sh}"
REACTORCIDE_JOBENVFILE="${REACTORCIDE_JOBENVFILE:-jobenv.sh}"
REACTORCIDE_WORKFLOW_CONTAINER_URL="${REACTORCIDE_WORKFLOW_CONTAINER_URL:-quay.io/catalystcommunity/catalyst-runner-image}"
REACTORCIDE_CLEAN_WORKSPACE="${REACTORCIDE_CLEAN_WORKSPACE:-./reactorcidetemp}"
REACTORCIDE_CORE_REPO_URL="${REACTORCIDE_CORE_REPO_URL:-git@github.com:catalystcommunity/reactorcide.git}"
REACTORCIDE_CORE_REF="{REACTORCIDE_CORE_REF:-main}"
REACTORCIDE_JOB_REF="{REACTORCIDE_JOB_REF:-main}"
REACTORCIDE_REPONAME="{REACTORCIDE_REPONAME:-jobrepo}"


# colors
TERMCOLOR_RED=$'\e[0;31m'
TERMCOLOR_GREEN=$'\e[0;32m'
TERMCOLOR_ORANGE=$'\e[0;33m'
TERMCOLOR_YELLOW=$'\e[1;33m'
TERMCOLOR_BLUE=$'\e[1;33m'
TERMCOLOR_CYAN=$'\e[0;36m'
TERMCOLOR_NONE=$'\e[0m'

# script internal vars
MISSING_DEPS="false"

log_status() {
  echo "${TERMCOLOR_GREEN}---------------------------------  ${1}  ---------------------------------${TERMCOLOR_NONE}"
}

external_cmd_check() {
    CMD="$1"
    if ! command -v ${CMD} 2>&1 >/dev/null
    then
        echo "'${CMD}' is not installed in the runner host, you'll have to do that first yourself"
        MISSING_DEPS="true"
    fi
}

external_check_required_env() {
    if [ "${REACTORCIDE_JOB_REPO_URL}" == "NOTSET" ]; then
        echo "REACTORCIDE_JOB_REPO_URL is not set"
        MISSING_DEPS="true"
    fi
}

# Modify for the help message
usage() {
    log_status "Usage"
    echo "${THISSCRIPT} command"
    echo ""
    echo "Commands:"
    echo ""
    echo "  run        (default) run the job with base assumptions"
    echo "  updatedb    Get the DB up to latest. Make sure it is running and your env vars are set"
    echo ""
    exit 0
}

external_run(){
    external_cmd_check "git"
    external_cmd_check "curl"
    external_cmd_check "docker"

    if [ ! -f "${REACTORCIDE_RUNNERENVFILE}" ]; then
        echo "${REACTORCIDE_RUNNERENVFILE} is unavailable"
        MISSING_DEPS="true"
    fi
    if [ ! -f "${REACTORCIDE_JOBENVFILE}"]; then
        echo "${REACTORCIDE_JOBENVFILE} is unavailable"
        MISSING_DEPS="true"
    fi

    if [ "${MISSING_DEPS}" == "true" ]; then
        echo "Missing dependencies, exiting."
        exit 1
    fi

    # Dependencies are there, source env vars and make sure requireds are set
    set -a
    source ${REACTORCIDE_RUNNERENVFILE}
    set +a

    external_check_required_env
    if [ "${MISSING_DEPS}" == "true" ]; then
        echo "Missing dependencies, exiting."
        exit 1
    fi

    # Everything in theory is working, so now just run the job

    # Yes, dangerous, we know, trust your devs or run this in a docker container somehow
    rm -rf ${REACTORCIDE_CLEAN_WORKSPACE}
    mkdir -p ${REACTORCIDE_CLEAN_WORKSPACE}/scratch
    cp ${REACTORCIDE_JOBENVFILE} ${REACTORCIDE_CLEAN_WORKSPACE}/
    cd ${REACTORCIDE_CLEAN_WORKSPACE}

    git clone ${REACTORCIDE_CORE_REPO_URL} reactorcide
    cd reactorcide
    git checkout ${REACTORCIDE_CORE_REF}
    cd -
    git clone ${REACTORCIDE_JOB_REPO_URL} ${REACTORCIDE_REPONAME}
    cd jobrepo
    git checkout ${REACTORCIDE_JOB_REF}
    cd -

    docker run -it --rm \
        --volume ./reactorcide:/reactorcide \
        --volume ./jobrepo:/workspace/${REACTORCIDE_REPONAME} \
        --volume ./scratch:/workspace/scratch \
        --volume ${REACTORCIDE_JOBENVFILE}:/workspace/jobenv.sh \
        ${REACTORCIDE_WORKFLOW_CONTAINER_URL} \
        --name reactorcide-job \
        /reactorcide/internal-root.sh /workspace/jobenv.sh

}

# This should be last in the script, all other functions are named beforehand.
case "$1" in
    "usage" | "-h" | "--help")
        shift
        usage "$@"
        ;;
    *)
        external_run
        ;;
esac

exit 0
