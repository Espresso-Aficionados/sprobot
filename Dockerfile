# set base image (host OS)
FROM python:3.8 as base

# set the working directory in the container
WORKDIR /code

# copy the dependencies file to the working directory
COPY requirements.txt .

# install dependencies
RUN pip install -r requirements.txt

FROM base as prod
ENV SPROBOT_ENV=prod
# copy the content of the local src directory to the working directory
COPY src/ .
CMD [ "python", "./sprobot/main.py" ]


# Dev stuff below here
FROM base as devbase
ENV SPROBOT_ENV=dev
COPY requirements-dev.txt .
RUN pip install -r requirements-dev.txt
# copy our test runner
COPY src/ .
COPY testing/ ./testing

from devbase as dev
CMD [ "python", "./sprobot/main.py" ]

FROM devbase as test
CMD ["/code/testing/run-tests.sh"]

FROM devbase as lint
CMD ["/code/testing/run-linters.sh"]
