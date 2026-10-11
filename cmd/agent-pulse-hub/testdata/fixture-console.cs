using System;
using System.Collections.Concurrent;
using System.Runtime.InteropServices;
using System.Threading;

// This independent fixture only handles console signals and reads JSON lines.
public static class FixtureConsole {
    public delegate bool Handler(uint kind);
    static Handler retained = kind => kind == 0;
    [DllImport("kernel32.dll")] static extern bool SetConsoleCtrlHandler(Handler handler, bool add);
    public static void Install() { if (!SetConsoleCtrlHandler(retained, true)) throw new Exception("console handler"); }
    static ConcurrentQueue<string> input = new ConcurrentQueue<string>();
    public static volatile bool Ended;
    public static void StartReader() {
        Thread reader = new Thread(() => {
            string line;
            while ((line = Console.In.ReadLine()) != null) input.Enqueue(line);
            Ended = true;
        });
        reader.IsBackground = true;
        reader.Start();
    }
    public static string Read() { string line; return input.TryDequeue(out line) ? line : null; }
}
