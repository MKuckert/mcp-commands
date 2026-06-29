#!/bin/bash

cd $1
pwd
echo "Checking for connected devices..."
DEVICE=$(adb devices | awk 'NR>1 {print $1}' | head -n 1)

if [ -z "$DEVICE" ]; then
    echo "No device found. Searching for available emulators..."
    
    # List available emulators and select the first one
    EMULATOR_NAME=$($ANDROID_HOME/emulator/emulator -list-avds | head -n 1)

    if [ -z "$EMULATOR_NAME" ]; then
        echo "No emulators found! Please create one using AVD Manager."
        exit 1
    fi

    echo "Starting emulator: $EMULATOR_NAME..."
    
    # Start the selected emulator in the background
    $ANDROID_HOME/emulator/emulator -avd "$EMULATOR_NAME" -no-window -no-audio -no-snapshot-load &

    echo "Waiting for emulator to boot..."
    adb wait-for-device
    
    # Wait until the emulator is fully booted
    while [[ "$(adb shell getprop sys.boot_completed | tr -d '\r')" != "1" ]]; do
        echo "Still booting..."
        sleep 5
    done
    
    DEVICE=$(adb devices | awk 'NR>1 {print $1}' | head -n 1)
fi

if [ -n "$DEVICE" ]; then
    MODEL=$(adb -s "$DEVICE" shell getprop ro.product.model)
    ANDROID_VERSION=$(adb -s "$DEVICE" shell getprop ro.build.version.release)
    echo "Using device: $MODEL (Android $ANDROID_VERSION) - $DEVICE"
else
    echo "Failed to detect or launch a device!"
    exit 1
fi

# Run Android tests
echo "Running Android tests on device: $DEVICE..."
./gradlew connectedAndroidTest
