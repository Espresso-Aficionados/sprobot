# sprobot
[![sprobot build, test, and maybe push](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml/badge.svg?branch=main)](https://github.com/Espresso-Aficionados/sprobot/actions/workflows/build-test-push.yaml)

Espresso Discord Profile Bot


## Quickstart:

Run tests locally using `./test.sh`, it will run the linter + tests for you in docker. 

Run the container itself by using `./run.sh`. This should automatically build and run a dev container for you. 

multiarch deployments are automatically built and pushed to dockerhub at [sadbox/sprobot](https://hub.docker.com/repository/docker/sadbox/sprobot) once a commit makes it to main. 

Keep dev/test-only dependencies in requirements-dev.txt, and production-necessary dependencies in requirements.txt. 

## For Contributors:

### Style:
This repository uses the [Black](https://github.com/psf/black) automatic formatter. 

### Linting:
We are using [flake8](https://flake8.pycqa.org/en/latest/), along with the [flake8-black](https://github.com/peterjc/flake8-black) plugin to enforce formatting before commit.

### Testing:
Tests are using [pytest](https://docs.pytest.org/en/7.1.x/)

### Updates:
The repo should have automatic pull requests submitted by dependabot when dependencies are updated. 
