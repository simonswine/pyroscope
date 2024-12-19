ARG SDK_VERSION=8.0
# The build images takes an SDK image of the buildplatform, so the platform the build is running on.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/dotnet/sdk:$SDK_VERSION AS build

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG SDK_VERSION

WORKDIR /dotnet

ADD example .

# Set the target framework to SDK_VERSION
RUN sed -i -E 's|<TargetFramework>.*</TargetFramework>|<TargetFramework>net'$SDK_VERSION'</TargetFramework>|' Example.csproj

# We hardcode linux-x64 here, as the profiler doesn't support any other platform
RUN dotnet publish -o . --framework net$SDK_VERSION --runtime linux-x64 --no-self-contained

# This fetches the SDK
FROM --platform=linux/amd64 pyroscope/pyroscope-dotnet:0.9.2-glibc AS sdk

# Runtime only image of the targetplatfrom, so the platform the image will be running on.
FROM --platform=linux/amd64 mcr.microsoft.com/dotnet/aspnet:$SDK_VERSION

WORKDIR /dotnet

COPY --from=build /dotnet/ ./

CMD sh -c "ASPNETCORE_URLS=http://*:${RIDESHARE_LISTEN_PORT} exec dotnet /dotnet/example.dll"
