# Reactorcide Design

The goal of any CI/CD system is to run a job. Some have a workflow definition, some have events and such, or any other number of capabilities, but at the core, a CI/CD job is a script you can run against a repo at a given point and have things happen. The rest is gluing up those extra bits.

Reactorcide originally was just a way of running a script from a checkout of a git repo. This new design is meant to separate out building blocks to make parallel jobs and events possible, with soome other pieces to do things like control concurrent runs and provide basic permissioning controls so an outside contributor can only run approved job definitions. The core, however, is just that ability to run a job at a given point. If you have that given point, you can run it on your laptop with the repo you have currently. We start there because this is the core concept.

## Status of this Design

This isn't finished at all. We'll evolve as we build the first real version on the branch this is starting from. Once this is on `main` that's when this is done and we'll adjust this doc if we remember.

## The Job

A job needs to be broken up into a few phases:
- Start a container, which as an event requires the container to have a startup "command" already in it
- Getting the code mounted, and the job code which could be separate
- Starting up the job
- Setting up the environment
- Running the job
- Cleanup and shutdown

Starting up is an event we can't have in the repo. It's part of the Reactorcide system. Whatever the "runner" is needs to have it or it's skipped.

Getting the code to be acted upon and the job code are not coupled to a VCS provider, they could be mounted into the container from a host, downloaded as a tarball, whatever. We want that step to be injected by env vars and handled by Reactorcide. This is critically different from starting up's constraints because one is the guarantees of the container, and one is the guarantees of a Reactorcide compliant setup. It could be baked into the container and just copied, but flexibility in this step is more useful.

Starting up the job is an event system. Do we need to run extra steps to ensure we're running as the right service account? Download extra assets? Install extra packages? Here's where we do that. It's like a mounting of things that aren't code.

Setting up the environment is similar, but it's but instead of the filesystem it's putting things into environment vars and perhaps spinning up external services for the job. Need a database? Need to ensure some user interaction with the job happens just before running it? We want that step separate so we can plug into it.

Running the job is simplest. We just call the job script. Default to something like `reactorcide_job.py` or something. It will have the Reactorcide libraries and extra plugins we wanted. That could be added from the code or a job code separately to the Reactorcide compliant code. Python paths can be awesome.

Cleanup and shutdown are hopefully obvious. Shut down our services, clean up sensitive things or cache or whatever. Release locks. I don't know. I have needed this in the past, so I know I'll need it in this somewhere.

Pretty much all of these are mostly optional except mounting the code and running the script. I need to know certain things in helper function, but that's not the job, that's the Reactorcide system. Separate concerns.

### The Workflow

There is no workflow! None. If we need to kick off other jobs, Reactorcide isn't in charge of managing that system. I'm not stupid, I want something simple not something that holds my hand and gives me a box to play in. I want to do whatever I want with minimal constraints. I can create other jobs, make a job depend on something else finishing first, and because we're using Corndogs we can have the ending state of this job end up as the start state for another job, and I can cache the results where I need to that makes sense.

Again, I define what I want in code, and if I want to have that in a nice single YAML file, that can just be in my code and I can parse it pretty easily. I'm not planning on making those functions in Reactorcide today, but it makes sense for later to have some parsing capabilities so if people (which I might be included in) want convenience we can add that. I could have a manual button press in the UI, send emails, whatever. Reactorcide just shouldn't be responsible for dictating that.

### Resources

That said, I want to run this in Kubernetes, and I want to do it in jobs. That way I can constrain resource, manage logs, etc more easily. The job coordinator can be the same pod as the API, it doesn't matter. Since we're using Corndogs I can already have N pods with any number of workers. Concurrency is fine. Jobs can have set resource requests/limits _per job_ as part of the payload, or default to none.

Easy peasy.

### The UI

I'm legitimately not concerned about the UI at all right now. It doesn't matter until we have fleshed out some use cases. Maybe I'll spin up something simple and stupid. I dunno. It runs separate from the API, that's all I care about.

### The DB

Corndogs needs a DB, and we'll have a separate DB. It will be postgres. They could be on the same box/pod/whatever. I don't need to limit that. Cloud Native just means I have a URL and a protocol, so as long as it's provided and working, we're good. Migrations are in the Helm deployment with the API.

### What about Security?

Operations require users. We have a placeholder for IDs we'll use with the IDP we're making, and some basic roles. That will be fine. Secrets in logs are a very different problem. That should be handled in the logger we're going to use in Reactorcide. We'll have a convention for marking secret env vars, and maybe we'll enhance that to provide utilities for later secrets that aren't env vars. I dunno. The logger will do the work. Print functions of subcommands from the main Python process will be captured from the process' stdout and stderr and logged accordingly, so I don't see this as an issue in subcommands either.

This is ultimately one of the core motivations of doing this project. I personally don't know of any CI/CD systems that allow third parties to submit a PR and have my approved job code run on their PR with some limited subset of secrets or capabilities unless I bake those protections into the job itself, especially not one I can self-host. I want this to work equally well for an Enterprise and an Open Source company with a wide arrange of adversarial actors. Exfiltrating secrets from CI is easy. Trusting that the job isn't going to inject them means they can run a PR 1000 different ways and they can't change the job code that gets those secrets in there at all.

## Services

There's only 3 services involved here.

Corndogs holds the tasks and their current status. We can determine all we'd ever care about from them, save it be logs, which will just be streams to files the Coordinator handles.

The Coordinator API is what ingests jobs and gets things submitted to corndogs and in the future any events that might need to be propogated out, but I'm unsure I'll ever do that until others might be using this and need live-updated UIs or something. I'm happy to refresh a TUI I make or whatever. Anyway, it's also the thing that will create the k8s jobs and such in separate coroutines.

Then there's the database. This is obvious.

The coordinator is bound to get a little complicated, but mostly for historical run purposes. If jobs get complicated, well, that's different.

## What about this library?

That's not a service, it's a library that also has scripts that get called. It will be in Python, and you can have "plugins" that just implement the functions you need, and you can copy them in at whatever stage. This is the "marrying" commentary about the Job itself. Reactorcide provides utilities and basic defaults to run stuff, but if you want to add extra "middleware" you can, and in whatever order. We'll go by name returned by some function in the module so we don't have to have `0000_absolutely_first_plugin` or `zzzzzz_really_run_this_last` stuff.

There will be "prerequisites" for that library that it's only on certain versions of Python and you need your VCS of choice on the PATH or whatever, but I'm not concerned with that today.

## Comments?

This design isn't completed. Talk to us in the Discord (you're on the Discord, right?) about changes or gotchas you're thinking of.