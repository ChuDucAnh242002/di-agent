Feature: Trust-aware peer routing
  The coordinator must prefer useful peers without routing work to peers
  whose trust has fallen below the configured floor.

  Scenario: A busy agent recommends a trusted lower-cost peer
    Given three healthy agent nodes are available
    And the busy agent has registered the candidate peers
    And peer "worker-2" has trust 0.8
    And peer "worker-3" has trust 0.8
    And peer "worker-2" has resource cost 0.2
    And peer "worker-3" has resource cost 0.4
    When the busy agent requests a recommendation
    Then it recommends peer "worker-2"
    And the recommendation contains expected savings
    And the recommendation contains a rationale

  Scenario: A peer below the trust floor is excluded
    Given three healthy agent nodes are available
    And the busy agent has registered the candidate peers
    And peer "worker-2" has trust 0.8
    And peer "worker-3" has trust 0.8
    And peer "worker-2" has resource cost 0.2
    And peer "worker-3" has resource cost 0.4
    When peer "worker-2" trust is changed to 0.15
    And the busy agent requests a recommendation
    Then it does not recommend peer "worker-2"
    And it recommends peer "worker-3"

  Scenario: No sufficiently trusted peer exists
    Given three healthy agent nodes are available
    And the busy agent has registered the candidate peers
    And peer "worker-2" has trust 0.2
    And peer "worker-3" has trust 0.4
    When the busy agent requests a recommendation
    Then the recommendation is rejected for insufficient trust