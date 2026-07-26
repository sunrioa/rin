using System;
using System.Collections;
using System.Text.Json;

namespace UnityEngine
{
    public class Object
    {
        protected static void Destroy(Object value) { }
        protected static void DontDestroyOnLoad(Object value) { }
    }
    public class GameObject : Object
    {
        public T AddComponent<T>() where T : MonoBehaviour, new() => new T();
    }
    public class MonoBehaviour : Object
    {
        public GameObject gameObject = new GameObject();
        protected object StartCoroutine(IEnumerator routine) => routine;
    }
    public class Transform : Object
    {
        public Vector3 position;
    }
    public struct Vector3
    {
        public float x;
        public float y;
        public float z;
    }
    [AttributeUsage(AttributeTargets.Field)]
    public sealed class SerializeField : Attribute { }
    [AttributeUsage(AttributeTargets.Field)]
    public sealed class RangeAttribute : Attribute
    {
        public RangeAttribute(float minimum, float maximum) { }
    }
    public static class Application
    {
        public static string persistentDataPath = "";
    }
    public static class Debug
    {
        public static void Log(object value) { }
        public static void LogWarning(object value) { }
        public static void LogError(object value) { }
    }
    public static class Time
    {
        public static float realtimeSinceStartup;
        public static int frameCount;
    }
    public sealed class WaitForSecondsRealtime
    {
        public WaitForSecondsRealtime(float seconds) { }
    }
    public static class JsonUtility
    {
        private static readonly JsonSerializerOptions Options =
            new JsonSerializerOptions { IncludeFields = true };
        public static string ToJson(object value) =>
            JsonSerializer.Serialize(value, value.GetType(), Options);
        public static T FromJson<T>(string value) =>
            JsonSerializer.Deserialize<T>(value, Options);
    }
}

namespace UnityEngine.AI
{
    public sealed class NavMeshAgent : UnityEngine.MonoBehaviour
    {
        public bool isOnNavMesh = true;
        public bool pathPending;
        public bool hasPath;
        public float remainingDistance;
        public float stoppingDistance;
        public bool SetDestination(UnityEngine.Vector3 destination)
        {
            hasPath = true;
            return true;
        }
        public void ResetPath()
        {
            hasPath = false;
        }
    }
}

namespace UnityEngine.SceneManagement
{
    public struct Scene
    {
        public int handle;
    }
    public enum LoadSceneMode
    {
        Single,
        Additive,
    }
    public static class SceneManager
    {
        private static Scene activeScene = new Scene { handle = 1 };
        public static event Action<Scene, LoadSceneMode> sceneLoaded;
        public static Scene GetActiveScene() => activeScene;
        public static void RaiseSceneLoaded(int handle)
        {
            activeScene = new Scene { handle = handle };
            sceneLoaded?.Invoke(activeScene, LoadSceneMode.Single);
        }
    }
}

namespace UnityEngine.Networking
{
    public class DownloadHandler : IDisposable
    {
        public virtual void Dispose() { }
    }
    public class DownloadHandlerScript : DownloadHandler
    {
        protected DownloadHandlerScript(byte[] buffer) { }
        protected virtual bool ReceiveData(byte[] data, int dataLength) => true;
    }
    public class UploadHandler : IDisposable
    {
        public virtual void Dispose() { }
    }
    public sealed class UploadHandlerRaw : UploadHandler
    {
        public UploadHandlerRaw(byte[] data) { }
    }
    public sealed class UnityWebRequest : IDisposable
    {
        public enum Result { InProgress, Success, ConnectionError, ProtocolError, DataProcessingError }
        public UploadHandler uploadHandler;
        public DownloadHandler downloadHandler;
        public int timeout;
        public int redirectLimit;
        public Result result;
        public long responseCode;
        public UnityWebRequest(string url, string method) { }
        public static string EscapeURL(string value) => Uri.EscapeDataString(value);
        public void SetRequestHeader(string name, string value) { }
        public object SendWebRequest() => null;
        public void Dispose()
        {
            uploadHandler?.Dispose();
            downloadHandler?.Dispose();
        }
    }
}
