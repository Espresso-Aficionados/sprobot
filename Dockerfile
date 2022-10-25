# set base image (host OS)
FROM python:3.11.0 as base

# set the working directory in the container
WORKDIR /code

RUN rm -f /etc/apt/apt.conf.d/docker-clean; echo 'Binary::apt::APT::Keep-Downloaded-Packages "true";' > /etc/apt/apt.conf.d/keep-cache
ARG DEBIAN_FRONTEND=noninteractive
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked apt update 
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked apt install -y uwsgi uwsgi-plugins-all

# copy the dependencies file to the working directory
COPY requirements.txt .

# install dependencies
RUN --mount=type=cache,target=/root/.cache pip install -r requirements.txt

FROM base as prod
ENV SPROBOT_ENV=prod
# copy the content of the local src directory to the working directory
COPY src/ .
CMD [ "python", "./sprobot/main.py" ]

from base as prodweb
WORKDIR /code/sprobot-web
CMD [ "uwsgi_python3", "--ini", "uwsgi.ini", "--http-socket", "0.0.0.0:80", "--pythonpath", "/usr/local/lib/python3.10/site-packages/"]

# Dev stuff below here
FROM base as devbase
ENV SPROBOT_ENV=dev
COPY requirements-dev.txt .
RUN --mount=type=cache,target=/root/.cache pip install -r requirements-dev.txt
# copy our test runner
COPY src/ .
COPY testing/ ./testing

from devbase as dev
CMD [ "python", "./sprobot/main.py" ]

from devbase as devweb
WORKDIR /code/sprobot-web
CMD [ "uwsgi_python3", "--ini", "uwsgi.ini", "--http-socket", "0.0.0.0:80", "--pythonpath", "/usr/local/lib/python3.10/site-packages/"]

FROM devbase as test
CMD ["/code/testing/run-tests.sh"]

FROM devbase as lint
CMD ["/code/testing/run-linters.sh"]
