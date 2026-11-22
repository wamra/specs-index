@FDS-001
Feature: Document upload

  @BRD-002 @smoke
  Scenario: Upload a single document
    Given I am signed in as an editor
    When I upload "quarterly-report.pdf" to the folder "Finance"
    Then the folder "Finance" lists "quarterly-report.pdf"

  @BRD-002
  Scenario: Reject an oversized file
    Given I am signed in as an editor
    When I upload a file larger than 500 MB
    Then the upload is rejected with "File is too large"
    And nothing is added to the folder

  @BRD-002
  Scenario Outline: Reject unsupported file types
    Given I am signed in as an editor
    When I upload a file named "<name>"
    Then the upload is rejected with "<message>"

    Examples:
      | name        | message                           |
      | setup.exe   | Executable files are not allowed  |
      | notes.xyz   | Unknown file type                 |
      | script.bat  | Executable files are not allowed  |

  Scenario: Show progress while a file uploads
    Given I am signed in as an editor
    When I upload a large file
    Then a progress bar shows the percentage sent
