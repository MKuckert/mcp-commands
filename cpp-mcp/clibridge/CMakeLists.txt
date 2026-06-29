cmake_minimum_required(VERSION 3.10)

include(FetchContent)

set(REPROC++ ON CACHE BOOL "Build C++ reproc library" FORCE)
set(REPROC_TESTS OFF CACHE BOOL "Build reproc tests" FORCE)
set(REPROC_INSTALL OFF CACHE BOOL "Install reproc" FORCE)

FetchContent_Declare(
  reproc
  GIT_REPOSITORY https://github.com/DaanDeMeyer/reproc.git
  GIT_TAG        v14.2.4
  EXCLUDE_FROM_ALL
)
FetchContent_MakeAvailable(reproc)

include_directories(${CMAKE_CURRENT_SOURCE_DIR})

set(TARGET clibridge)
add_executable(${TARGET} main.cpp)
target_link_libraries(${TARGET} PRIVATE mcp)
target_include_directories(${TARGET} PRIVATE ${CMAKE_SOURCE_DIR}/include)
if(OPENSSL_FOUND)
    target_link_libraries(${TARGET} PRIVATE ${OPENSSL_LIBRARIES})
endif()
