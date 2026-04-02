using System;
using System.Collections.Generic;

namespace Example;

internal class OrderService
{
    public void FindNearestVehicle(long searchRadius, string vehicle)
    {
        lock (_lock)
        {
            var labels = Pyroscope.LabelSet.Empty.BuildUpon()
                .Add("vehicle", vehicle)
                .Build();
            Pyroscope.LabelsWrapper.Do(labels, () =>
            {
                for (long i = 0; i < searchRadius * 1000000000; i++)
                {
                }

                AllocateMemory(searchRadius * 1000);

                if (vehicle.Equals("car"))
                {
                    CheckDriverAvailability(labels, searchRadius);
                }
            });
        }
    }

    private readonly object _lock = new();

    private static void AllocateMemory(long count)
    {
        var buffers = new List<byte[]>((int)count);
        for (long i = 0; i < count; i++)
        {
            var buf = new byte[1024];
            buf[0] = (byte)(i & 0xFF); // prevent dead-code elimination
            buffers.Add(buf);
        }
        GC.KeepAlive(buffers);
    }

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

            AllocateMemory(searchRadius * 2000);

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