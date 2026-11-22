@FDS-002
Feature: Bulk upload

  Background:
    Given I am signed in as an editor
    And the folder "Inbox" is empty

  @BRD-003 @smoke
  Scenario: Upload a folder of documents
    When I drop a folder with 20 documents onto "Inbox"
    Then "Inbox" lists 20 documents

  @BRD-004 @wip
  Scenario: Resume an interrupted upload
    Given I dropped a folder with 20 documents onto "Inbox"
    And the connection dropped after 8 documents
    When I reconnect and resume the upload
    Then only the remaining 12 documents are sent

  Scenario: Skip duplicates in a batch
    Given "Inbox" already holds "budget.xlsx"
    When I drop a folder containing "budget.xlsx" onto "Inbox"
    Then the duplicate is skipped and reported
