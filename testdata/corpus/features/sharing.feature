@FDS-009
Feature: Share links

  @BRD-013 @smoke
  Scenario: Create an expiring share link
    Given I own the document "Proposal"
    When I create a share link that expires in 7 days
    Then the link opens "Proposal" read-only

  @BRD-013
  Scenario: Revoke a share link
    Given a share link for "Proposal" exists
    When I revoke the link
    Then opening the link shows a not-found page

  @BRD-014 @wip
  Scenario: Disabled external sharing blocks new links
    Given an administrator disabled external sharing
    When I try to create a share link
    Then the action is refused with "External sharing is off"

  Scenario: Share link opens a read-only preview
    Given a share link for "Proposal" exists
    When an outside visitor opens the link
    Then the visitor sees no edit controls
