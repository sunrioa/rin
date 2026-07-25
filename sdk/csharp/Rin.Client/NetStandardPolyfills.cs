#if NETSTANDARD2_0
namespace System.Runtime.CompilerServices
{
    internal static class IsExternalInit
    {
    }
}
#endif

namespace Rin.Client
{
    internal static class Guard
    {
        internal static T NotNull<T>(T? value, string name) where T : class =>
            value ?? throw new ArgumentNullException(name);
    }
}
