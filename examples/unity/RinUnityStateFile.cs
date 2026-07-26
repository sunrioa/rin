using System;
using System.IO;
using System.Text;
using UnityEngine;

internal sealed class RinUnityStateFile
{
    private const int MaxStateBytes = 1024 * 1024;
    private readonly string path;

    public RinUnityStateFile(string path)
    {
        this.path = path;
    }

    public bool PrimaryExists
    {
        get { return File.Exists(path); }
    }

    public bool TemporaryExists
    {
        get { return File.Exists(path + ".tmp"); }
    }

    public DurableState Load()
    {
        return Load(path);
    }

    public DurableState LoadBackup()
    {
        return Load(path + ".bak");
    }

    public bool Save(DurableState state)
    {
        var temporary = path + ".tmp";
        var backup = path + ".bak";
        try
        {
            var directory = Path.GetDirectoryName(path);
            if (string.IsNullOrEmpty(directory)) return false;
            Directory.CreateDirectory(directory);
            var bytes = Encoding.UTF8.GetBytes(JsonUtility.ToJson(state));
            if (bytes.Length <= 0 || bytes.Length > MaxStateBytes) return false;
            using (var stream = new FileStream(
                temporary,
                FileMode.Create,
                FileAccess.Write,
                FileShare.None))
            {
                stream.Write(bytes, 0, bytes.Length);
                stream.Flush(true);
            }
            if (File.Exists(path))
            {
                if (File.Exists(backup)) File.Delete(backup);
                File.Replace(temporary, path, backup);
            }
            else
            {
                File.Move(temporary, path);
            }
            return true;
        }
        catch (Exception error)
        {
            Debug.LogError("Could not persist Rin state: " + error.Message);
            return false;
        }
    }

    private static DurableState Load(string source)
    {
        try
        {
            if (!File.Exists(source)) return null;
            var info = new FileInfo(source);
            if (info.Length <= 0 || info.Length > MaxStateBytes) return null;
            return JsonUtility.FromJson<DurableState>(
                File.ReadAllText(source, Encoding.UTF8));
        }
        catch (Exception)
        {
            return null;
        }
    }
}
