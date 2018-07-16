# set author and base
# need to use fedora 27 until glide is fixed in fedora 28
# (or we get an alternative glide)
FROM fedora:27
MAINTAINER Heketi Developers <heketi-devel@gluster.org>

LABEL version="1.3.1"
LABEL description="Development build"

# let's setup all the necessary environment variables
ENV BUILD_HOME=/build
ENV GOPATH=$BUILD_HOME/golang
ENV PATH=$GOPATH/bin:$PATH
ENV HEKETI_BRANCH="release/8"

# install dependencies, build and cleanup
RUN mkdir $BUILD_HOME $GOPATH && \
    dnf -y install glide golang git make glibc-static && \
    dnf -y install glide golang git make mercurial && \
    dnf -y clean all && \
    mkdir -p $GOPATH/src/github.com/cloud-tools && \
    cd $GOPATH/src/github.com/cloud-tools && \
    git clone -b $HEKETI_BRANCH https://github.com/cloud-tools/heketi.git && \
    cd $GOPATH/src/github.com/cloud-tools/heketi && \
    glide install -v && \
    make && \
    cp heketi /usr/bin/heketi && \
    cp client/cli/go/heketi-cli /usr/bin/heketi-cli && \
    glide cc && \
    cd && rm -rf $BUILD_HOME && \
    dnf -y remove git glide golang mercurial && \
    dnf -y autoremove && \
    dnf -y clean all

# post install config and volume setup
ADD ./heketi.json /etc/heketi/heketi.json
ADD ./heketi-start.sh /usr/bin/heketi-start.sh
VOLUME /etc/heketi

RUN mkdir /var/lib/heketi
VOLUME /var/lib/heketi

# expose port, set user and set entrypoint with config option
ENTRYPOINT ["/usr/bin/heketi-start.sh"]
EXPOSE 8080
