cmake_minimum_required(VERSION 3.20)
project(rotor-controller-host-tests C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

set(SM_SRC ${CMAKE_CURRENT_SOURCE_DIR}/../../controller/src)

add_executable(test_state_machine
    test_state_machine.c
    ${SM_SRC}/state_machine.c
)
target_include_directories(test_state_machine PRIVATE ${SM_SRC})
target_compile_options(test_state_machine PRIVATE -Wall -Wextra -g)

add_executable(test_protocol
    test_protocol.c
    ${SM_SRC}/protocol.c
    ${SM_SRC}/state_machine.c
)
target_include_directories(test_protocol PRIVATE ${SM_SRC})
target_compile_options(test_protocol PRIVATE -Wall -Wextra -g)

enable_testing()
add_test(NAME state_machine COMMAND test_state_machine)
add_test(NAME protocol       COMMAND test_protocol)
