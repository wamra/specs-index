@FDS-011
Feature: Retention and holds

  @BRD-016
  Scenario: Retention expiry archives a document
    Given a policy that archives documents after 365 days
    When a document turns 366 days old
    Then the document is moved to the archive overnight

  @BRD-017 @wip
  Scenario: Legal hold blocks deletion
    Given a document under legal hold
    When retention would delete it
    Then the document is kept and the hold is logged

  @BRD-016
  Scenario: Retention report lists expiring documents
    Given three documents expire next week
    When I open the retention report
    Then the three documents are listed with their dates
