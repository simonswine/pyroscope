using System;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Collections;
using System.Runtime;

using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Builder;
using Microsoft.Extensions.DependencyInjection;

using OpenTelemetry.Trace;

using Pyroscope.OpenTelemetry;

namespace Example;

public static class Program
{
    private static readonly List<FileStream> Files = new();
    public const string CustomActivitySourceName = "Example.ScooterService";
    public static void Main(string[] args)
    {
        for (int i = 0; i < 1024; i++)
        {
            Files.Add(File.Open("/dev/null", FileMode.Open, FileAccess.Read, FileShare.Read));
        }
        object globalLock = new();
        var strings = new List<string>();

        // Background thread: log whenever a GC collection occurs per generation
        var gcThread = new Thread(() =>
        {
            int[] counts = new int[GC.MaxGeneration + 1];
            for (int g = 0; g <= GC.MaxGeneration; g++)
                counts[g] = GC.CollectionCount(g);

            while (true)
            {
                Thread.Sleep(100);
                for (int g = 0; g <= GC.MaxGeneration; g++)
                {
                    int current = GC.CollectionCount(g);
                    if (current != counts[g])
                    {
                        long heapBytes = GC.GetTotalMemory(forceFullCollection: false);
                        Console.WriteLine(
                            $"[GC] Gen{g} collection #{current}  heap={heapBytes / 1024 / 1024} MB  " +
                            $"time={DateTime.UtcNow:HH:mm:ss.fff}");
                        counts[g] = current;
                    }
                }
            }
        })
        {
            IsBackground = true,
            Name = "GCMonitor",
        };
        gcThread.Start();

        var orderService = new OrderService();
        var bikeService = new BikeService(orderService);
        var scooterService = new ScooterService(orderService);
        var carService = new CarService(orderService);

        var builder = WebApplication.CreateBuilder(args);
        builder.Services.AddOpenTelemetry()
            .WithTracing(b =>
            {
                b
                .AddAspNetCoreInstrumentation()
                .AddSource(CustomActivitySourceName)
                .AddConsoleExporter()
                .AddOtlpExporter()
                .AddProcessor(new PyroscopeSpanProcessor());
            });
        var app = builder.Build();

        app.MapGet("/bike", () =>
        {
            bikeService.Order(1);
            return "Bike ordered";
        });
        app.MapGet("/scooter", () =>
        {
            scooterService.Order(2);
            return "Scooter ordered";
        });

        app.MapGet("/car", () =>
        {
            carService.Order(3);
            return "Car ordered";
        });
        
        
        app.MapGet("/pyroscope/cpu", (HttpRequest request) =>
        {
            var enable = request.Query["enable"] == "true";
            Pyroscope.Profiler.Instance.SetCPUTrackingEnabled(enable);
            return "OK";
        });
        app.MapGet("/pyroscope/allocation", (HttpRequest request) =>
        {
            var enable = request.Query["enable"] == "true";
            Pyroscope.Profiler.Instance.SetAllocationTrackingEnabled(enable);
            return "OK";
        });
        app.MapGet("/pyroscope/contention", (HttpRequest request) =>
        {
            var enable = request.Query["enable"] == "true";
            Pyroscope.Profiler.Instance.SetContentionTrackingEnabled(enable);
            return "OK";
        });
        app.MapGet("/pyroscope/exception", (HttpRequest request) =>
        {
            var enable = request.Query["enable"] == "true";
            Pyroscope.Profiler.Instance.SetExceptionTrackingEnabled(enable);
            return "OK";
        });
        
        
        app.MapGet("/playground/allocation", (HttpRequest request) =>
        {
            var strings = new List<string>();
            for (var i = 0; i < 10000; i++)
            {
                strings.Add("foobar" + i);
            }

            return "OK";
        });
        app.MapGet("/playground/contention", (HttpRequest request) =>
        {
            for (var i = 0; i < 100; i++)
            {
                lock (globalLock)
                {
                    Thread.Sleep(10);
                }
            }
            return "OK";
        });
        app.MapGet("/playground/exception", (HttpRequest request) =>
        {
            for (var i = 0; i < 1000; i++)
            {
                try
                {
                    throw new Exception("foobar" + i);
                }
                catch (Exception)
                {
                }
            }
            return "OK";
        });
        app.MapGet("/playground/leak", (HttpRequest request) =>
        {
            for (var i = 0; i < 1000; i++)
            {
                strings.Add("leak " + i);
            }
            return "OK";
        });
        app.MapGet("/", () =>
        {
            string env = "";
            foreach (DictionaryEntry e in System.Environment.GetEnvironmentVariables())
            {
                env += e.Key + " = " + e.Value + "<br>\n";
            }
            return env;
        });

        app.Run();
    }
}
