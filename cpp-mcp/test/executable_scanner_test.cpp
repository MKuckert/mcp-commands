#include <gtest/gtest.h>
#include <filesystem>
#include <fstream>
#include <vector>
#include <string>
#include <algorithm>
#include <chrono>
#include "../clibridge/executable_scanner.h"

class ExecutableScannerTest : public ::testing::Test {
protected:
    std::filesystem::path temp_dir_;

    void SetUp() override {
        // Create a unique temporary directory
        auto system_temp = std::filesystem::temp_directory_path();
        auto timestamp = std::to_string(std::chrono::system_clock::now().time_since_epoch().count());
        temp_dir_ = system_temp / ("clibridge_test_scanner_" + timestamp);
        std::filesystem::create_directories(temp_dir_);
    }

    void TearDown() override {
        // Clean up the temporary directory
        if (std::filesystem::exists(temp_dir_)) {
            std::filesystem::remove_all(temp_dir_);
        }
    }

    void create_file(const std::filesystem::path& rel_path, bool make_executable = false) {
        auto full_path = temp_dir_ / rel_path;
        std::filesystem::create_directories(full_path.parent_path());
        
        std::ofstream out(full_path);
        out << "#!/bin/sh\necho 'hello'" << std::endl;
        out.close();

        if (make_executable) {
#ifdef _WIN32
            // On Windows, scanner uses extension (.exe, .bat, .cmd, .ps1)
#else
            std::filesystem::permissions(full_path, 
                std::filesystem::perms::owner_exec, 
                std::filesystem::perm_options::add);
#endif
        }
    }
};

TEST_F(ExecutableScannerTest, NonExistentDirThrows) {
    auto non_existent = temp_dir_ / "does_not_exist";
    EXPECT_THROW(scan_executables(non_existent), std::runtime_error);
}

TEST_F(ExecutableScannerTest, EmptyDirReturnsEmpty) {
    auto results = scan_executables(temp_dir_);
    EXPECT_TRUE(results.empty());
}

TEST_F(ExecutableScannerTest, ExcludesNonExecutables) {
    create_file("file1.txt", false);
    create_file("sub/file2.log", false);

    auto results = scan_executables(temp_dir_);
    EXPECT_TRUE(results.empty());
}

TEST_F(ExecutableScannerTest, DetectsExecutables) {
#ifdef _WIN32
    create_file("script.bat", true);
    create_file("sub/program.exe", true);
    create_file("non_exec.txt", false);
#else
    create_file("script.sh", true);
    create_file("sub/program", true);
    create_file("non_exec.txt", false);
#endif

    auto results = scan_executables(temp_dir_);
    ASSERT_EQ(results.size(), 2);

    // Verify paths are returned correctly
    std::vector<std::string> tool_names;
    for (const auto& [name, path] : results) {
        tool_names.push_back(name);
        EXPECT_TRUE(std::filesystem::exists(path));
        EXPECT_TRUE(path.is_absolute());
    }

    std::sort(tool_names.begin(), tool_names.end());
#ifdef _WIN32
    EXPECT_EQ(tool_names[0], "script.bat");
    EXPECT_EQ(tool_names[1], "sub/program.exe");
#else
    EXPECT_EQ(tool_names[0], "script.sh");
    EXPECT_EQ(tool_names[1], "sub/program");
#endif
}
