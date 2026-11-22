@FDS-006
Feature: Version history

  @BRD-008 @smoke
  Scenario: List the versions of a document
    Given a document saved three times
    When I open its version history
    Then three versions are listed, newest first

  @BRD-009
  Rule: Only editors can restore a version

    @id:SCN-101
    Scenario: Editor restores an earlier version
      Given I am signed in as an editor
      And a document with three versions
      When I restore version 1
      Then version 1 content is shown as the current document

    Scenario: Viewer cannot restore a version
      Given I am signed in as a viewer
      When I open the version history
      Then no restore button is shown

    @wip
    Scenario: Restoring creates a new version
      Given I am signed in as an editor
      And a document with three versions
      When I restore version 1
      Then the history lists four versions
