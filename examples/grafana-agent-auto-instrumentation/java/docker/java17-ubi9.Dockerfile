FROM registry.access.redhat.com/ubi9/openjdk-17@sha256:dca1dec9331efd56320b658b931e53b234e19b545b8c130ea40155c72fea8ab4

ADD ./FastSlow.java ./FastSlow.java
RUN javac FastSlow.java

CMD ["java", "FastSlow"]
