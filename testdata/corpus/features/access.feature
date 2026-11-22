Feature: Access control

  @FDS-007 @BRD-011 @id:SCN-102
  Scenario: Sign in through the corporate identity provider
    Given I am not signed in
    When I choose "Sign in with company account"
    Then I am returned to the portal as the authenticated employee

  @FDS-008 @BRD-012 @smoke
  Scenario: Viewer cannot edit a document
    Given I am signed in as a viewer
    When I open a document
    Then the edit button is disabled

  @FDS-008 @BRD-012
  Scenario: Admin assigns a role to a group
    Given I am signed in as an admin
    When I give the group "Finance" the editor role on "Budget"
    Then members of "Finance" can edit "Budget"

  Scenario: Warn before a session times out
    Given I have been idle for 25 minutes
    Then a warning offers to extend the session
