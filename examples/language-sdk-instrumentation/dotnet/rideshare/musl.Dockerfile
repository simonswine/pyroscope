ARG SDK_VERSION=8.0
# The build images takes an SDK image of the buildplatform, so the platform the build is running on.
FROM --platform=$BUILDPLATFORM mcr.microsoft.com/dotnet/sdk:$SDK_VERSION-alpine AS build

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG SDK_VERSION

WORKDIR /dotnet/src

ADD example/BikeService.cs \
    example/CarService.cs \
    example/Example.csproj \
    example/OrderService.cs \
    example/Program.cs \
    example/ScooterService.cs ./

# Set the target framework to SDK_VERSION
RUN sed -i -E 's|<TargetFramework>.*</TargetFramework>|<TargetFramework>net'$SDK_VERSION'</TargetFramework>|' Example.csproj

# We hardcode linux-x64 here, as the profiler doesn't support any other platform.
# Publish to a separate directory: .NET 10+ cleans the output dir before compiling,
# so -o . (same as source dir) would delete source files and cause CS5001.
RUN dotnet publish -o /dotnet/publish --framework net$SDK_VERSION --runtime linux-musl-x64 --no-self-contained

# This fetches the SDK
FROM --platform=linux/amd64 pyroscope/pyroscope-dotnet:0.14.3-musl AS sdk

# Runtime only image of the targetplatfrom, so the platform the image will be running on.
FROM --platform=linux/amd64 mcr.microsoft.com/dotnet/aspnet:$SDK_VERSION-alpine

WORKDIR /dotnet

COPY --from=sdk /Pyroscope.Profiler.Native.so ./Pyroscope.Profiler.Native.so
COPY --from=sdk /Pyroscope.Linux.ApiWrapper.x64.so ./Pyroscope.Linux.ApiWrapper.x64.so
COPY --from=build /dotnet/publish/ ./


ENV CORECLR_ENABLE_PROFILING=1
ENV CORECLR_PROFILER={BD1A650D-AC5D-4896-B64F-D6FA25D6B26A}
ENV CORECLR_PROFILER_PATH=/dotnet/Pyroscope.Profiler.Native.so
ENV LD_PRELOAD=/dotnet/Pyroscope.Linux.ApiWrapper.x64.so

ENV PYROSCOPE_APPLICATION_NAME=rideshare.dotnet.app
ENV PYROSCOPE_SERVER_ADDRESS=http://pyroscope:4040
ENV PYROSCOPE_LOG_LEVEL=debug
ENV PYROSCOPE_PROFILING_ENABLED=1
ENV PYROSCOPE_PROFILING_ALLOCATION_ENABLED=true
ENV PYROSCOPE_PROFILING_CONTENTION_ENABLED=true
ENV PYROSCOPE_PROFILING_EXCEPTION_ENABLED=true
ENV PYROSCOPE_PROFILING_HEAP_ENABLED=true
ENV RIDESHARE_LISTEN_PORT=5000


CMD sh -c "ASPNETCORE_URLS=http://*:${RIDESHARE_LISTEN_PORT} exec dotnet /dotnet/example.dll"
