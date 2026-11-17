FROM alpine:3.22
RUN apk add --no-cache bash ca-certificates tzdata
ENV TZ=Asia/Shanghai
WORKDIR /apserver
COPY apserver/ /apserver/
EXPOSE 8080
CMD ["bash", "/apserver/docker_run.sh"]
