#ifndef CLIBRIDGE_EXECUTABLE_SCANNER_H
#define CLIBRIDGE_EXECUTABLE_SCANNER_H

#include <vector>
#include <string>
#include <utility>
#include <filesystem>
#include <stdexcept>
#include <algorithm>
#include <cctype>

/**
 * @brief Scans a directory recursively for executable files.
 * @param scan_root The root directory to scan.
 * @return A vector of pairs, where each pair contains the relative tool name
 *         (using generic forward-slash path separators) and its absolute path.
 * @throws std::runtime_error if scan_root does not exist or is not a directory.
 */
inline std::vector<std::pair<std::string, std::filesystem::path>>
scan_executables(const std::filesystem::path& scan_root) {
    namespace fs = std::filesystem;

    if (!fs::exists(scan_root) || !fs::is_directory(scan_root)) {
        throw std::runtime_error("Scan root does not exist or is not a directory: " + scan_root.string());
    }

    std::vector<std::pair<std::string, fs::path>> result;
    std::error_code ec;

    // Default directory options do not follow directory symlinks
    auto it = fs::recursive_directory_iterator(scan_root, fs::directory_options::none, ec);
    if (ec) {
        throw std::runtime_error("Failed to open directory iterator: " + ec.message());
    }

    for (const auto& entry : it) {
        std::error_code entry_ec;
        auto status = entry.status(entry_ec);
        if (entry_ec) {
            continue; // Skip entries whose status cannot be retrieved
        }

        if (!fs::is_regular_file(status)) {
            continue; // Only regular files can be tools
        }

        bool is_executable = false;
#ifdef _WIN32
        // TODO: On Windows, owner_exec is unreliable; add an extension-based check
        std::string ext = entry.path().extension().string();
        std::transform(ext.begin(), ext.end(), ext.begin(), [](unsigned char c) {
            return std::tolower(c);
        });
        if (ext == ".exe" || ext == ".bat" || ext == ".cmd" || ext == ".ps1") {
            is_executable = true;
        }
#else
        if ((status.permissions() & fs::perms::owner_exec) != fs::perms::none) {
            is_executable = true;
        }
#endif

        if (is_executable) {
            std::error_code rel_ec;
            auto rel_path = fs::relative(entry.path(), scan_root, rel_ec);
            if (!rel_ec) {
                std::error_code abs_ec;
                auto abs_path = fs::absolute(entry.path(), abs_ec);
                if (!abs_ec) {
                    result.push_back({rel_path.generic_string(), abs_path});
                } else {
                    result.push_back({rel_path.generic_string(), entry.path()});
                }
            }
        }
    }

    return result;
}

#endif // CLIBRIDGE_EXECUTABLE_SCANNER_H
