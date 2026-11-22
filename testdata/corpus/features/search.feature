@FDS-003 @FDS-005
Feature: Document search

  @BRD-006 @smoke
  Scenario: Exact title wins over text matches
    Given documents titled "Budget" and "Budget review"
    When I search for "Budget"
    Then the first result is titled "Budget"

  @FDS-004 @BRD-007
  Scenario: Filter results by status
    Given an approved and a draft document about "pricing"
    When I search for "pricing status:approved"
    Then only the approved document is listed

  @BRD-007
  Scenario: Excluded tag hides matching results
    Given a document tagged "archive" about "pricing"
    When I search for "pricing -tag:archive"
    Then the tagged document is not listed

  @wip
  Scenario: Show a message when nothing matches
    When I search for "zzzzqqqq"
    Then the page says "No documents found"

  Scenario: Search inside one folder
    Given matching documents in "Finance" and "Legal"
    When I search for "contract" in the folder "Legal"
    Then only documents from "Legal" are listed
