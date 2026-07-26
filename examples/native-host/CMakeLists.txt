cmake_minimum_required(VERSION 3.16)
project(rin_native_host C)

add_library(rin_host STATIC src/rin_host.c)
target_include_directories(rin_host PUBLIC include)
target_compile_features(rin_host PUBLIC c_std_99)

if(MSVC)
  target_compile_options(rin_host PRIVATE /W4 /WX)
else()
  target_compile_options(rin_host PRIVATE -Wall -Wextra -Wpedantic -Werror)
endif()

add_executable(rin_host_test tests/test_rin_host.c)
target_link_libraries(rin_host_test PRIVATE rin_host)
if(MSVC)
  target_compile_options(rin_host_test PRIVATE /W4 /WX)
else()
  target_compile_options(rin_host_test PRIVATE -Wall -Wextra -Wpedantic -Werror)
endif()

enable_testing()
add_test(NAME rin_host_contract COMMAND rin_host_test)
