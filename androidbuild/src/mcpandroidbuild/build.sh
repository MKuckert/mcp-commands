#!/bin/bash
# Description: Performs a gradle build on the specified directory
cd $1
pwd
./gradlew build
