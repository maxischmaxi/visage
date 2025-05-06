# Visage

This Project tries to optimize the process of regression testing by using MD5, SHA1 and blockhashes instead of saving the whole image.
The goal is to save time and space by only saving the differences between the images.

## Installation

```bash
git clone https://github.com/maxischmaxi/visage.git
cd visage
go install
```

## Usage

Visage is expecting the project you want to test to be a storybook project.
You can use the `visage check` command to run the tests.

Visage will start storybook and use a headless browser to take screenshots of the components.
It automatically detects all stories and takes screenshots of them.
It will then compare the screenshots to the previous ones and save the differences.
