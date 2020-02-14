FROM alpine:3.8

ADD deployment/webhook-create-signed-cert.sh /bin/webhook-create-signed-cert.sh
RUN chmod +x /bin/webhook-create-signed-cert.sh

ADD deployment/webhook-patch-ca-bundle.sh /bin/webhook-patch-ca-bundle.sh
RUN chmod +x /bin/webhook-patch-ca-bundle.sh

