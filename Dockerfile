FROM golang

WORKDIR /

ENTRYPOINT ["app"]

ADD app /bin
