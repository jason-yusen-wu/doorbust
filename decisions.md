**Auth provider**: why Amazon Cognito?
* Pros: easy integration with AWS, generous free tier
* Cons: requires Lambda triggers for customization

Alternatives:
* Okta: complex enterprise requirements, out of scope for this project, expensive scaling
* Neon auth: stores auth on DB, but introduces more DB contention during bursty logins

**Hosting**: Why AWS EC2?
* Pros: a dedicated EC2 instance removes hardware confounding factors that might occur with shared/serverless ECS.
* Cons: No ALB. But since the project currently only uses one EC2 instance, ALB provides no meaningful benefits while still having a steep pricepoint.



