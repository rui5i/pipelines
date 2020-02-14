#!/bin/bash

# Generate certificate key for cache server
./webhook-create-signed-cert.sh
echo "signed certificate key generated for execution cache"

# Patch CA_BUNDLE for execution-cache-configmap.yaml
