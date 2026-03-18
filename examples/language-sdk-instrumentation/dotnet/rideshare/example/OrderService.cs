using System;
using System.Collections.Generic;

namespace Example;

internal class OrderService
{
    // Simulates a growing route cache — accumulates over time to produce
    // a realistic heap allocation profile visible in memory profiling.
    private static readonly List<byte[]> RouteCache = new();
    private static readonly object CacheLock = new();

    private static void CacheRouteData(string vehicle, long searchRadius)
    {
        // Allocate ~64 KB of route coordinate data per search
        var routeData = new byte[64 * 1024];
        var rng = new Random();
        rng.NextBytes(routeData);

        lock (CacheLock)
        {
            RouteCache.Add(routeData);
            // Keep at most 2000 entries (~128 MB) to avoid OOM in demo
            if (RouteCache.Count > 2000)
                RouteCache.RemoveAt(0);
        }
    }

    public void FindNearestVehicle(long searchRadius, string vehicle)
    {
        lock (_lock)
        {
            var labels = Pyroscope.LabelSet.Empty.BuildUpon()
                .Add("vehicle", vehicle)
                .Build();
            Pyroscope.LabelsWrapper.Do(labels, () =>
            {
                // Allocate per-request route computation buffers
                CacheRouteData(vehicle, searchRadius);

                for (long i = 0; i < searchRadius * 1000000000; i++)
                {
                }

                if (vehicle.Equals("car"))
                {
                    CheckDriverAvailability(labels, searchRadius);
                }
            });
        }
    }

    private readonly object _lock = new();

    private static void CheckDriverAvailability(Pyroscope.LabelSet ctx, long searchRadius)
    {
        var region = System.Environment.GetEnvironmentVariable("REGION") ?? "unknown_region";
        ctx = ctx.BuildUpon()
            .Add("driver_region", region)
            .Build();
        Pyroscope.LabelsWrapper.Do(ctx, () =>
        {
            for (long i = 0; i < searchRadius * 1000000000; i++)
            {
            }

            var now = DateTime.Now.Minute % 2 == 0;
            var forceMutexLock = DateTime.Now.Minute % 2 == 0;
            if ("eu-north".Equals(region) && forceMutexLock)
            {
                MutexLock(searchRadius);
            }
        });
    }

    private static void MutexLock(long searchRadius)
    {
        for (long i = 0; i < 30 * searchRadius * 1000000000; i++)
        {
        }
    }
}