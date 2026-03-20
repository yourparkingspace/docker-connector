# Docker Connector

Super simple utility that facilities direct-to-bash with a Docker container running in ECS.

## Download

Boldly assuming anyone that needs this has homebrew already installed.

```
curl -L https://github.com/yourparkingspace/docker-connector/releases/latest/download/docker-connector -o /opt/homebrew/bin/docker-connector && chmod +x /opt/homebrew/bin/docker-connector
```

## Usage

You should already be authenticated with AWS, that could be through tools like `saml2aws`.

```
docker-connector \
    --cluster <cluster-name> \
    --service <service-name> \
    --container <container-name> \
    [--profile <aws-profile>] # If you have specified a profile during your AWS login.
```
