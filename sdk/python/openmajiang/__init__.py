"""Private-observation Bot SDK; long-lived API keys never enter URLs or logs."""
from .client import BotClient, DecisionEngine, StrategyContext, first_legal

__all__ = ["BotClient", "DecisionEngine", "StrategyContext", "first_legal"]
