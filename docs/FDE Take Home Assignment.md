# **Objective** 

You are building an **Agentic AI system** that helps an Ad Agency manage podcast content more efficiently. Given **raw podcast transcripts** , the agent should: 

1. **Summarize** each episode clearly and concisely. 2. **Extract key takeaways and quotes** that are suitable for publishing as show notes or social media highlights. 

3. **Proof-check factual claims** in the episode (using retrieval or reasoning on a knowledge base or Web Search). 

Your goal is to: 

1. Demonstrate your ability to design, build, and reason about a **multi-step AI agent** that autonomously plans and executes tasks. 

2. Demonstrate your ability to design and reason about a **robust cloud deployment strategy** . 

# **Input** 

You will be given: 

- **podcast transcript files** ( `.txt` or `.json` ) Each contains: 

   - Host and guest dialogues 

   - Timestamps 

   - Occasional filler words or transcription errors 

Example (Format will be similar to the below): 

[00:02:14] Host: So NASA recently announced something about a new Mars mission. [00:02:18] Guest: Yes, it’s launching in early 2026 — they’re planning to bring back samples. 

[00:03:01] Host: Wow, that’s exciting. 

## **Tasks** 

### **1. Summarization** 

Generate a **summary** of each episode (~200–300 words) that captures: 

- Core themes 

- Key discussions 

- Outcomes or opinions shared 

### **2. Key Notes Extraction** 

Produce a structured list of: 

- 🔹 **Top 5 takeaways** 

- 💬 **Notable quotes** (with timestamps) 

- 🧭 **Topics discussed** (tag-style labels) 

### **3. Fact-Checking (Agentic Component)** 

Identify factual statements (e.g., “NASA Mars mission launches in 2026”) and **verify them** using one or more of: 

- A retrieval or search API (you can mock this) 

- A local knowledge base file you create 

- A reasoning step simulating verification 

Mark each as: 

- ✅ Verified true 

- ⚠ Possibly outdated/inaccurate 

- ❓ Unverifiable 

Example output: 

**Claim Verification Confidence** NASA’s Mars mission launches in 2026 True (confirmed by mock NASA DB) 0.92 Bitcoin will reach $200K by 2025 Unverifiable 0.40 

## 4 **.Deployment Strategy** 

In addition to your implementation, **describe how you would deploy this agentic system for real-world use** by an Ad Agency. 

You **do not have to implement the deployment** , but your answer should include both a diagram and a narrative description explaining how your solution handles **deployment, cost, scalability, fault tolerance, and other typical non-functional considerations** 

## **Deliverables** 

Please deliver the following via a publicly accessible URL (e.g. GitHub, Google Drive). Do not respond with an email attachment; our email system will not allow it through. 

- Source code for your solution, containerized and able to be run locally via Docker 

- • The model should be available locally or on a cloud provider (AWS Bedrock, OpenAI, Anthropic, etc.) 

- Detailed Readme about how to run your solution 

- Readable logs or console outputs showing **agent reasoning steps** or **plans** (not just final results). 

- Output JSON or Markdown file with: 

   - Summary 

   - Notes 

   - Fact-check table 

- Detailed Deployment Strategy `o` Diagram 

   - Narrative description 

